package storagecapacityprioritization

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/volumebinding"
	"k8s.io/kubernetes/pkg/scheduler/framework/runtime"

	"github.com/bells17/storage-capacity-prioritization-scheduler/pkg/apis/config"
)

var (
	waitProvisioner      = "wait"
	immediate            = storagev1.VolumeBindingImmediate
	waitForFirstConsumer = storagev1.VolumeBindingWaitForFirstConsumer
	immediateSC          = &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "immediate-sc",
		},
		VolumeBindingMode: &immediate,
	}
	waitSC = &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wait-sc",
		},
		VolumeBindingMode: &waitForFirstConsumer,
		Provisioner:       waitProvisioner,
	}
	waitHDDSC = &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wait-hdd-sc",
		},
		VolumeBindingMode: &waitForFirstConsumer,
		Provisioner:       waitProvisioner,
	}
	waitCSIDriver = &storagev1.CSIDriver{
		ObjectMeta: metav1.ObjectMeta{
			Name: waitProvisioner,
		},
		Spec: storagev1.CSIDriverSpec{
			StorageCapacity: (func() *bool {
				b := true
				return &b
			})(),
		},
	}
)

type pluginTester struct {
	plugin            *StorageCapacityPrioritization
	framework         framework.Framework
	nodeInfos         []*framework.NodeInfo
	filteredNodeInfos []*framework.NodeInfo
}

func newPluginTester(t *testing.T, ctx context.Context, nodes []*v1.Node, pvcs []*v1.PersistentVolumeClaim, pvs []*v1.PersistentVolume, cscs []*storagev1beta1.CSIStorageCapacity, args *config.StorageCapacityPrioritizationArgs) (*pluginTester, error) {
	client := fake.NewSimpleClientset()
	informerFactory := informers.NewSharedInformerFactory(client, 0)
	opts := []runtime.Option{
		runtime.WithClientSet(client),
		runtime.WithInformerFactory(informerFactory),
	}
	fh, err := runtime.NewFramework(nil, nil, opts...)
	if err != nil {
		return nil, err
	}

	if args == nil {
		args = &config.StorageCapacityPrioritizationArgs{}
	}

	pl, err := New(args, fh)
	if err != nil {
		return nil, err
	}

	t.Log("Feed testing data and wait for them to be synced")
	client.StorageV1().CSIDrivers().Create(ctx, waitCSIDriver, metav1.CreateOptions{})
	client.StorageV1().StorageClasses().Create(ctx, immediateSC, metav1.CreateOptions{})
	client.StorageV1().StorageClasses().Create(ctx, waitSC, metav1.CreateOptions{})
	client.StorageV1().StorageClasses().Create(ctx, waitHDDSC, metav1.CreateOptions{})
	for _, node := range nodes {
		_, err := client.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
	}
	for _, pvc := range pvcs {
		_, err := client.CoreV1().PersistentVolumeClaims(pvc.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
	}
	for _, pv := range pvs {
		_, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
	}
	for _, csc := range cscs {
		_, err := client.StorageV1beta1().CSIStorageCapacities(csc.Namespace).Create(ctx, csc, metav1.CreateOptions{})
		if err != nil {
			return nil, err
		}
	}

	t.Log("Start informer factory after initialization")
	informerFactory.Start(ctx.Done())

	t.Log("Wait for all started informers' cache were synced")
	informerFactory.WaitForCacheSync(ctx.Done())
	t.Log("Verify")

	nodeInfos := make([]*framework.NodeInfo, 0)
	for _, node := range nodes {
		nodeInfo := framework.NewNodeInfo()
		nodeInfo.SetNode(node)
		nodeInfos = append(nodeInfos, nodeInfo)
	}
	return &pluginTester{
		plugin:    pl.(*StorageCapacityPrioritization),
		framework: fh,
		nodeInfos: nodeInfos,
	}, nil
}

func (pl *pluginTester) PreFilter(t *testing.T, ctx context.Context, pod *v1.Pod, state *framework.CycleState, expect *framework.Status) {
	t.Logf("Verify: call PreFilter and check status")
	result := pl.plugin.PreFilter(context.Background(), state, pod)
	if !reflect.DeepEqual(result, expect) {
		t.Errorf("prefilter status does not match: %v, want: %v", result, expect)
	}
	return
}

func (pl *pluginTester) Filter(t *testing.T, ctx context.Context, pod *v1.Pod, state *framework.CycleState, expects []*framework.Status) {
	t.Logf("Verify: call Filter and check status")
	for i, nodeInfo := range pl.nodeInfos {
		result := pl.plugin.Filter(ctx, state, pod, nodeInfo)
		if !reflect.DeepEqual(result, expects[i]) {
			t.Errorf("filter status does not match for node %q, got: %+v, want: %+v", nodeInfo.Node().Name, result, expects[i])
		}
		if result == nil {
			pl.filteredNodeInfos = append(pl.filteredNodeInfos, nodeInfo)
		}
	}
	return
}

func (pl *pluginTester) PreScore(t *testing.T, ctx context.Context, pod *v1.Pod, state *framework.CycleState, expect *framework.Status) {
	t.Logf("Verify: call PreScore and check status")
	var nodes []*v1.Node
	for _, nodeInfo := range pl.filteredNodeInfos {
		nodes = append(nodes, nodeInfo.Node())
	}
	result := pl.plugin.PreScore(ctx, state, pod, nodes)
	if !reflect.DeepEqual(result, expect) {
		t.Errorf("prescore status does not match got: %+v, want: %+v", result, expect)
	}
	return
}

func (pl *pluginTester) Score(t *testing.T, ctx context.Context, pod *v1.Pod, state *framework.CycleState, expects []*framework.Status, expectScores []int64) {
	t.Logf("Verify: call Score and check status")
	for i, node := range pl.filteredNodeInfos {
		score, result := pl.plugin.Score(ctx, state, pod, node.Node().Name)
		if !reflect.DeepEqual(result, expects[i]) {
			t.Errorf("filter status does not match got: %+v, want: %+v", result, expects[i])
		}
		if score != expectScores[i] {
			t.Errorf("score result does not match got: %d, want: %+d", score, expectScores[i])
		}
	}
	return
}

func TestStorageCapacityPrioritizationPreFilter(t *testing.T) {
	table := []struct {
		name        string
		pod         *v1.Pod
		nodes       []*v1.Node
		pvcs        []*v1.PersistentVolumeClaim
		pvs         []*v1.PersistentVolume
		cscs        []*storagev1beta1.CSIStorageCapacity
		args        *config.StorageCapacityPrioritizationArgs
		state       *framework.CycleState
		expect      *framework.Status
		expectState *framework.CycleState
	}{
		{
			name: "PreFilter",
			state: (func() *framework.CycleState {
				return framework.NewCycleState()
			})(),
			expect: nil,
			expectState: (func() *framework.CycleState {
				state := framework.NewCycleState()
				state.Write(stateKey, &stateData{storageClassNames: sets.NewString()})
				return state
			})(),
		},
	}
	for _, item := range table {
		t.Run(item.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tester, err := newPluginTester(t, ctx, item.nodes, item.pvcs, item.pvs, item.cscs, item.args)
			if err != nil {
				return
			}
			tester.PreFilter(t, ctx, item.pod, item.state, item.expect)
			if !reflect.DeepEqual(item.state, item.expectState) {
				t.Errorf("prefilter cycle state does not match: %v, want: %v", item.state, item.expectState)
			}
		})
	}
}

func TestStorageCapacityPrioritizationFilter(t *testing.T) {
	table := []struct {
		name        string
		pod         *v1.Pod
		nodes       []*v1.Node
		pvcs        []*v1.PersistentVolumeClaim
		pvs         []*v1.PersistentVolume
		cscs        []*storagev1beta1.CSIStorageCapacity
		args        *config.StorageCapacityPrioritizationArgs
		state       *framework.CycleState
		expects     []*framework.Status
		expectState *framework.CycleState
	}{
		{
			name: "pod has not pvcs",
			pod:  makePod("pod-a").Pod,
			nodes: []*v1.Node{
				makeNode("node-a").Node,
			},
			expects: (func() []*framework.Status {
				return []*framework.Status{nil}
			})(),
			state: (func() *framework.CycleState {
				state := framework.NewCycleState()
				state.Write(framework.StateKey(volumebinding.Name), &volumebinding.StateData{})
				state.Write(stateKey, &stateData{storageClassNames: sets.NewString()})
				return state
			})(),
			expectState: (func() *framework.CycleState {
				state := framework.NewCycleState()
				state.Write(framework.StateKey(volumebinding.Name), &volumebinding.StateData{})
				state.Write(stateKey, &stateData{storageClassNames: sets.NewString()})
				return state
			})(),
		},
		{
			name: "pod has unbound waitForConsumer pvcs",
			pod:  makePod("pod-a").withPVCVolume("pvc-a", "").Pod,
			nodes: []*v1.Node{
				makeNode("zone-a-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-a").Node,
				makeNode("zone-b-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-b").Node,
				makeNode("zone-c-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-c").Node,
			},
			pvcs: []*v1.PersistentVolumeClaim{
				makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim,
			},
			cscs: []*storagev1beta1.CSIStorageCapacity{
				makeCSC("1", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-a",
				})).CSIStorageCapacity,
				makeCSC("2", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-b",
				})).CSIStorageCapacity,
				makeCSC("3", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-c",
				})).CSIStorageCapacity,
			},
			expects: (func() []*framework.Status {
				return []*framework.Status{nil, nil, nil}
			})(),
			state: (func() *framework.CycleState {
				state := framework.NewCycleState()
				podVolumes := map[string]*volumebinding.PodVolumes{
					"zone-a-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
					"zone-b-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
					"zone-c-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
				}
				state.Write(framework.StateKey(volumebinding.Name), volumebinding.FakeStateData(nil, podVolumes))
				state.Write(stateKey, &stateData{storageClassNames: sets.NewString()})
				return state
			})(),
			expectState: (func() *framework.CycleState {
				state := framework.NewCycleState()
				podVolumes := map[string]*volumebinding.PodVolumes{
					"zone-a-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
					"zone-b-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
					"zone-c-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
				}
				state.Write(framework.StateKey(volumebinding.Name), volumebinding.FakeStateData(nil, podVolumes))
				state.Write(stateKey, &stateData{
					storageClassNames: sets.NewString(waitSC.Name),
				})
				return state
			})(),
		},
		{
			name: "pod has unbound waitForConsumer pvcs - (contain not enough capacity nodes)",
			pod:  makePod("pod-a").withPVCVolume("pvc-a", "").Pod,
			nodes: []*v1.Node{
				makeNode("zone-a-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-a").Node,
				makeNode("zone-b-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-b").Node,
				makeNode("zone-c-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-c").Node,
			},
			pvcs: []*v1.PersistentVolumeClaim{
				makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim,
			},
			cscs: []*storagev1beta1.CSIStorageCapacity{
				makeCSC("1", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-a",
				})).CSIStorageCapacity,
				makeCSC("2", waitSC.Name).withCapacity(resource.MustParse("49Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-b",
				})).CSIStorageCapacity,
				makeCSC("3", waitSC.Name).withCapacity(resource.MustParse("49Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-c",
				})).CSIStorageCapacity,
			},
			expects: (func() []*framework.Status {
				return []*framework.Status{
					nil,
					(func() *framework.Status {
						q := resource.MustParse("50Gi")
						return framework.NewStatus(framework.UnschedulableAndUnresolvable, fmt.Sprintf("there is nothing enough capacities of csi storage capacity objects. node=%q sizeInBytes=%d", "zone-b-node-a", (&q).Value()))
					})(),
					(func() *framework.Status {
						q := resource.MustParse("50Gi")
						return framework.NewStatus(framework.UnschedulableAndUnresolvable, fmt.Sprintf("there is nothing enough capacities of csi storage capacity objects. node=%q sizeInBytes=%d", "zone-c-node-a", (&q).Value()))
					})(),
				}
			})(),
			state: (func() *framework.CycleState {
				state := framework.NewCycleState()
				podVolumes := map[string]*volumebinding.PodVolumes{
					"zone-a-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
					"zone-b-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
					"zone-c-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
				}
				state.Write(framework.StateKey(volumebinding.Name), volumebinding.FakeStateData(nil, podVolumes))
				state.Write(stateKey, &stateData{storageClassNames: sets.NewString()})
				return state
			})(),
			expectState: (func() *framework.CycleState {
				state := framework.NewCycleState()
				podVolumes := map[string]*volumebinding.PodVolumes{
					"zone-a-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
					"zone-b-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
					"zone-c-node-a": {
						DynamicProvisions: []*v1.PersistentVolumeClaim{makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim},
					},
				}
				state.Write(framework.StateKey(volumebinding.Name), volumebinding.FakeStateData(nil, podVolumes))
				state.Write(stateKey, &stateData{
					storageClassNames: sets.NewString(waitSC.Name),
				})
				return state
			})(),
		},
	}
	for _, item := range table {
		t.Run(item.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tester, err := newPluginTester(t, ctx, item.nodes, item.pvcs, item.pvs, item.cscs, item.args)
			if err != nil {
				return
			}
			tester.Filter(t, ctx, item.pod, item.state, item.expects)
			if !reflect.DeepEqual(item.state, item.expectState) {
				t.Errorf("filter cycle state does not match: %v, want: %v", item.state, item.expectState)
			}
		})
	}
}

func TestStorageCapacityPrioritizationPreScore(t *testing.T) {
	table := []struct {
		name        string
		pod         *v1.Pod
		nodes       []*v1.Node
		pvcs        []*v1.PersistentVolumeClaim
		pvs         []*v1.PersistentVolume
		cscs        []*storagev1beta1.CSIStorageCapacity
		args        *config.StorageCapacityPrioritizationArgs
		state       *framework.CycleState
		expect      *framework.Status
		expectState *framework.CycleState
	}{
		{
			name: "pod has unbound waitForConsumer pvcs",
			pod:  makePod("pod-a").withPVCVolume("pvc-a", "").Pod,
			nodes: []*v1.Node{
				makeNode("zone-a-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-a").Node,
				makeNode("zone-b-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-b").Node,
				makeNode("zone-c-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-c").Node,
			},
			pvcs: []*v1.PersistentVolumeClaim{
				makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim,
			},
			cscs: []*storagev1beta1.CSIStorageCapacity{
				makeCSC("1", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-a",
				})).CSIStorageCapacity,
				makeCSC("2", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-b",
				})).CSIStorageCapacity,
				makeCSC("3", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-c",
				})).CSIStorageCapacity,
			},
			expect: nil,
			state: (func() *framework.CycleState {
				state := framework.NewCycleState()
				claimsToBind := []*v1.PersistentVolumeClaim{
					makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim,
				}
				state.Write(framework.StateKey(volumebinding.Name), volumebinding.FakeStateData(claimsToBind, nil))
				state.Write(stateKey, &stateData{
					storageClassNames: sets.NewString(waitSC.Name),
				})
				return state
			})(),
			expectState: (func() *framework.CycleState {
				state := framework.NewCycleState()
				claimsToBind := []*v1.PersistentVolumeClaim{
					makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("50Gi")).PersistentVolumeClaim,
				}
				state.Write(framework.StateKey(volumebinding.Name), volumebinding.FakeStateData(claimsToBind, nil))
				state.Write(stateKey, &stateData{
					storageClassNames: sets.NewString(waitSC.Name),
					scores: map[string]int64{
						"zone-a-node-a": 100,
						"zone-b-node-a": 100,
						"zone-c-node-a": 100,
					},
				})
				return state
			})(),
		},
		{
			name: "pod has unbound waitForConsumer pvcs - 2",
			pod:  makePod("pod-a").withPVCVolume("pvc-a", "").Pod,
			nodes: []*v1.Node{
				makeNode("zone-a-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-a").Node,
				makeNode("zone-b-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-b").Node,
				makeNode("zone-c-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-c").Node,
			},
			pvcs: []*v1.PersistentVolumeClaim{
				makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("25Gi")).PersistentVolumeClaim,
			},
			cscs: []*storagev1beta1.CSIStorageCapacity{
				makeCSC("1", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-a",
				})).CSIStorageCapacity,
				makeCSC("2", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-b",
				})).CSIStorageCapacity,
				makeCSC("3", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-c",
				})).CSIStorageCapacity,
			},
			expect: nil,
			state: (func() *framework.CycleState {
				state := framework.NewCycleState()
				claimsToBind := []*v1.PersistentVolumeClaim{
					makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("25Gi")).PersistentVolumeClaim,
				}
				state.Write(framework.StateKey(volumebinding.Name), volumebinding.FakeStateData(claimsToBind, nil))
				state.Write(stateKey, &stateData{
					storageClassNames: sets.NewString(waitSC.Name),
				})
				return state
			})(),
			expectState: (func() *framework.CycleState {
				state := framework.NewCycleState()
				claimsToBind := []*v1.PersistentVolumeClaim{
					makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("25Gi")).PersistentVolumeClaim,
				}
				state.Write(framework.StateKey(volumebinding.Name), volumebinding.FakeStateData(claimsToBind, nil))
				state.Write(stateKey, &stateData{
					storageClassNames: sets.NewString(waitSC.Name),
					scores: map[string]int64{
						"zone-a-node-a": 50,
						"zone-b-node-a": 50,
						"zone-c-node-a": 50,
					},
				})
				return state
			})(),
		},
		{
			name: "pod has unbound waitForConsumer pvcs(multi storage class)",
			pod:  makePod("pod-a").withPVCVolume("pvc-a", "").withPVCVolume("pvc-b", "").Pod,
			nodes: []*v1.Node{
				makeNode("zone-a-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-a").Node,
				makeNode("zone-b-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-b").Node,
				makeNode("zone-c-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-c").Node,
			},
			pvcs: []*v1.PersistentVolumeClaim{
				makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("20Gi")).PersistentVolumeClaim,
				makePVC("pvc-b", waitHDDSC.Name).withRequestStorage(resource.MustParse("10Gi")).PersistentVolumeClaim,
			},
			cscs: []*storagev1beta1.CSIStorageCapacity{
				makeCSC("1", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-a",
				})).CSIStorageCapacity,
				makeCSC("2", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-b",
				})).CSIStorageCapacity,
				makeCSC("3", waitSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-c",
				})).CSIStorageCapacity,
				makeCSC("4", waitHDDSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-a",
				})).CSIStorageCapacity,
				makeCSC("5", waitHDDSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-b",
				})).CSIStorageCapacity,
				makeCSC("6", waitHDDSC.Name).withCapacity(resource.MustParse("50Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-c",
				})).CSIStorageCapacity,
			},
			expect: nil,
			state: (func() *framework.CycleState {
				state := framework.NewCycleState()
				claimsToBind := []*v1.PersistentVolumeClaim{
					makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("20Gi")).PersistentVolumeClaim,
					makePVC("pvc-b", waitHDDSC.Name).withRequestStorage(resource.MustParse("10Gi")).PersistentVolumeClaim,
				}
				state.Write(framework.StateKey(volumebinding.Name), volumebinding.FakeStateData(claimsToBind, nil))
				state.Write(stateKey, &stateData{
					storageClassNames: sets.NewString(waitSC.Name, waitHDDSC.Name),
				})
				return state
			})(),
			expectState: (func() *framework.CycleState {
				state := framework.NewCycleState()
				claimsToBind := []*v1.PersistentVolumeClaim{
					makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("20Gi")).PersistentVolumeClaim,
					makePVC("pvc-b", waitHDDSC.Name).withRequestStorage(resource.MustParse("10Gi")).PersistentVolumeClaim,
				}
				state.Write(framework.StateKey(volumebinding.Name), volumebinding.FakeStateData(claimsToBind, nil))
				state.Write(stateKey, &stateData{
					storageClassNames: sets.NewString(waitSC.Name, waitHDDSC.Name),
					scores: map[string]int64{
						"zone-a-node-a": 30,
						"zone-b-node-a": 30,
						"zone-c-node-a": 30,
					},
				})
				return state
			})(),
		},
	}
	for _, item := range table {
		t.Run(item.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tester, err := newPluginTester(t, ctx, item.nodes, item.pvcs, item.pvs, item.cscs, item.args)
			if err != nil {
				return
			}
			tester.filteredNodeInfos = tester.nodeInfos
			tester.PreScore(t, ctx, item.pod, item.state, item.expect)
			if !reflect.DeepEqual(item.state, item.expectState) {
				t.Errorf("prescore cycle state does not match: %v, want: %v", item.state, item.expectState)
			}
		})
	}
}

func TestStorageCapacityPrioritizationScore(t *testing.T) {
	table := []struct {
		name         string
		nodes        []*v1.Node
		state        *framework.CycleState
		expects      []*framework.Status
		expectScores []int64
	}{
		{
			name: "Score",
			nodes: []*v1.Node{
				makeNode("zone-a-node-a").withLabel("topology.kubernetes.io/zone", "zone-a").Node,
				makeNode("zone-b-node-a").withLabel("topology.kubernetes.io/zone", "zone-b").Node,
				makeNode("zone-c-node-a").withLabel("topology.kubernetes.io/zone", "zone-c").Node,
			},
			state: (func() *framework.CycleState {
				state := framework.NewCycleState()
				state.Write(stateKey, &stateData{
					scores: map[string]int64{
						"zone-a-node-a": 10,
						"zone-b-node-a": 20,
						"zone-c-node-a": 30,
					},
				})
				return state
			})(),
			expects:      []*framework.Status{nil, nil, nil},
			expectScores: []int64{10, 20, 30},
		},
	}
	for _, item := range table {
		t.Run(item.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tester, err := newPluginTester(t, ctx, nil, nil, nil, nil, nil)
			if err != nil {
				return
			}
			tester.filteredNodeInfos = tester.nodeInfos
			tester.Score(t, ctx, makePod("pod-a").Pod, item.state, item.expects, item.expectScores)
		})
	}
}

func TestStorageCapacityPrioritization(t *testing.T) {
	table := []struct {
		name                        string
		pod                         *v1.Pod
		nodes                       []*v1.Node
		pvcs                        []*v1.PersistentVolumeClaim
		pvs                         []*v1.PersistentVolume
		cscs                        []*storagev1beta1.CSIStorageCapacity
		args                        *config.StorageCapacityPrioritizationArgs
		volumebindigPreFilterStatus *framework.Status
		preFilterStatus             *framework.Status
		volumebindigFilterStatus    []*framework.Status
		filterStatus                []*framework.Status
		preScoreStatus              *framework.Status
		scoreStatus                 []*framework.Status
		expectScores                []int64
	}{
		{
			name: "pod has unbound waitForConsumer pvcs",
			pod:  makePod("pod-a").withPVCVolume("pvc-a", "").Pod,
			nodes: []*v1.Node{
				makeNode("zone-a-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-a").Node,
				makeNode("zone-b-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-b").Node,
				makeNode("zone-c-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-c").Node,
				makeNode("zone-d-node-a").
					withLabel("topology.kubernetes.io/zone", "zone-d").Node,
			},
			pvcs: []*v1.PersistentVolumeClaim{
				makePVC("pvc-a", waitSC.Name).withRequestStorage(resource.MustParse("10Gi")).PersistentVolumeClaim,
			},
			cscs: []*storagev1beta1.CSIStorageCapacity{
				makeCSC("1", waitSC.Name).withCapacity(resource.MustParse("10Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-a",
				})).CSIStorageCapacity,
				makeCSC("2", waitSC.Name).withCapacity(resource.MustParse("20Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-b",
				})).CSIStorageCapacity,
				makeCSC("3", waitSC.Name).withCapacity(resource.MustParse("100Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-c",
				})).CSIStorageCapacity,
				makeCSC("4", waitSC.Name).withCapacity(resource.MustParse("5Gi")).withTopology(labels.Set(map[string]string{
					"topology.kubernetes.io/zone": "zone-d",
				})).CSIStorageCapacity,
			},
			volumebindigPreFilterStatus: nil,
			preFilterStatus:             nil,
			volumebindigFilterStatus:    []*framework.Status{nil, nil, nil, nil},
			filterStatus: []*framework.Status{nil, nil, nil, func() *framework.Status {
				q := resource.MustParse("10Gi")
				status := framework.NewStatus(framework.UnschedulableAndUnresolvable)
				status.AppendReason(fmt.Sprintf("there is nothing enough capacities of csi storage capacity objects. node=%q sizeInBytes=%d", "zone-d-node-a", (&q).Value()))
				return status
			}()},
			preScoreStatus: nil,
			scoreStatus:    []*framework.Status{nil, nil, nil},
			expectScores:   []int64{100, 50, 10},
		},
	}
	for _, item := range table {
		t.Run(item.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tester, err := newPluginTester(t, ctx, item.nodes, item.pvcs, item.pvs, item.cscs, item.args)
			if err != nil {
				return
			}

			args := &schedulerconfig.VolumeBindingArgs{
				BindTimeoutSeconds: 300,
				Shape: []schedulerconfig.UtilizationShapePoint{
					{
						Utilization: 0,
						Score:       0,
					},
					{
						Utilization: 100,
						Score:       int32(schedulerconfig.MaxCustomPriorityScore),
					},
				},
			}
			pl, err := volumebinding.New(args, tester.framework, feature.Features{
				EnableVolumeCapacityPriority: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			vbplugin := pl.(*volumebinding.VolumeBinding)
			state := framework.NewCycleState()

			t.Log("Start informer factory after initialization(for volumebinding infomers)")
			tester.framework.SharedInformerFactory().Start(ctx.Done())

			t.Log("Wait for all started informers' cache were synced(for volumebinding infomers)")
			tester.framework.SharedInformerFactory().WaitForCacheSync(ctx.Done())
			t.Log("Verify")

			t.Logf("Verify: call VolumeBinding PreFilter and check status")
			volumebindigPreFilterStatus := vbplugin.PreFilter(ctx, state, item.pod)
			if !reflect.DeepEqual(volumebindigPreFilterStatus, item.volumebindigPreFilterStatus) {
				t.Errorf("volumebinding prefilter status does not match: %v, want: %v", volumebindigPreFilterStatus, item.volumebindigPreFilterStatus)
			}
			tester.PreFilter(t, ctx, item.pod, state, item.preFilterStatus)
			t.Logf("Verify: call VolumeBinding Filter and check status")
			for i, nodeInfo := range tester.nodeInfos {
				gotStatus := vbplugin.Filter(ctx, state, item.pod, nodeInfo)
				if !reflect.DeepEqual(gotStatus, item.volumebindigFilterStatus[i]) {
					t.Errorf("volumebinding filter status does not match for node %q, got: %v, want: %v", nodeInfo.Node().Name, gotStatus, item.volumebindigFilterStatus[i])
				}
			}
			tester.Filter(t, ctx, item.pod, state, item.filterStatus)
			tester.PreScore(t, ctx, item.pod, state, item.preScoreStatus)
			tester.Score(t, ctx, item.pod, state, item.scoreStatus, item.expectScores)
		})
	}
}
