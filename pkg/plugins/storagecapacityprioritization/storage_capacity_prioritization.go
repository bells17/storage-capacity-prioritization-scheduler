package storagecapacityprioritization

import (
	"context"
	"errors"
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/api/storage/v1beta1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	storagelistersv1beta1 "k8s.io/client-go/listers/storage/v1beta1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/volumebinding"

	"github.com/bells17/storage-capacity-prioritization-scheduler/pkg/apis/config"
)

const (
	Name                        = "StorageCapacityPrioritization"
	stateKey framework.StateKey = Name
)

type claimGroup []*v1.PersistentVolumeClaim

func (cg *claimGroup) totalRequiredCapacity() (int64, error) {
	total := resource.Quantity{}
	for _, claim := range *cg {
		quantity, ok := claim.Spec.Resources.Requests[v1.ResourceStorage]
		if !ok {
			return 0, fmt.Errorf("claim %s/%s does't have a resource request", claim.GetName(), claim.GetNamespace())
		}
		total.Add(quantity)
	}
	return total.Value(), nil
}

type claimsByStorageClass map[string]claimGroup

type stateData struct {
	storageClassNames sets.String
	scores            map[string]int64
	sync.Mutex
}

func (d *stateData) Clone() framework.StateData {
	return d
}

func getStateData(cs *framework.CycleState) (*stateData, error) {
	state, err := cs.Read(stateKey)
	if err != nil {
		return nil, err
	}
	s, ok := state.(*stateData)
	if !ok {
		return nil, errors.New("unable to convert state into stateData")
	}
	return s, nil
}

func validateStorageCapacityPrioritizationArgs(path *field.Path, args *config.StorageCapacityPrioritizationArgs) error {
	var allErrs field.ErrorList
	return allErrs.ToAggregate()
}

func New(plArgs runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	args, err := getArgs(plArgs)
	if err != nil {
		return nil, err
	}
	if err := validateStorageCapacityPrioritizationArgs(nil, &args); err != nil {
		return nil, err
	}

	return &StorageCapacityPrioritization{
		args:                     args,
		classLister:              handle.SharedInformerFactory().Storage().V1().StorageClasses().Lister(),
		csiDriverLister:          handle.SharedInformerFactory().Storage().V1().CSIDrivers().Lister(),
		csiStorageCapacityLister: handle.SharedInformerFactory().Storage().V1beta1().CSIStorageCapacities().Lister(),
	}, nil
}

func getArgs(obj runtime.Object) (config.StorageCapacityPrioritizationArgs, error) {
	if obj == nil {
		return config.StorageCapacityPrioritizationArgs{}, nil
	}
	ptr, ok := obj.(*config.StorageCapacityPrioritizationArgs)
	if !ok {
		klog.Errorf("the args is not StorageCapacityPrioritizationArgs type: %v", obj)
		return config.StorageCapacityPrioritizationArgs{}, fmt.Errorf("want args to be of type StorageCapacityPrioritizationArgs, got %T", obj)
	}
	return *ptr, nil
}

type StorageCapacityPrioritization struct {
	args                     config.StorageCapacityPrioritizationArgs
	classLister              storagelisters.StorageClassLister
	csiDriverLister          storagelisters.CSIDriverLister
	csiStorageCapacityLister storagelistersv1beta1.CSIStorageCapacityLister
}

var _ framework.FilterPlugin = &StorageCapacityPrioritization{}
var _ framework.PreScorePlugin = &StorageCapacityPrioritization{}
var _ framework.ScorePlugin = &StorageCapacityPrioritization{}
var _ framework.EnqueueExtensions = &StorageCapacityPrioritization{}

func (pl *StorageCapacityPrioritization) Name() string {
	return Name
}

func (pl *StorageCapacityPrioritization) EventsToRegister() []framework.ClusterEvent {
	return []framework.ClusterEvent{
		// Pods may fail because of missing or mis-configured storage class
		// (e.g., allowedTopologies, volumeBindingMode), and hence may become
		// schedulable upon StorageClass Add or Update events.
		{Resource: framework.StorageClass, ActionType: framework.Add | framework.Update},
		// We bind PVCs with PVs, so any changes may make the pods schedulable.
		{Resource: framework.PersistentVolumeClaim, ActionType: framework.Add | framework.Update},
		{Resource: framework.PersistentVolume, ActionType: framework.Add | framework.Update},
		// Pods may fail to find available PVs because the node labels do not
		// match the storage class's allowed topologies or PV's node affinity.
		// A new or updated node may make pods schedulable.
		{Resource: framework.Node, ActionType: framework.Add | framework.Update},
		// We rely on CSI node to translate in-tree PV to CSI.
		{Resource: framework.CSINode, ActionType: framework.Add | framework.Update},
		{Resource: framework.CSIDriver, ActionType: framework.Add | framework.Update},
		{Resource: framework.CSIStorageCapacity, ActionType: framework.Add | framework.Update},
	}
}

// PreFilter invoked at the prefilter extension point to check if pod has all
// immediate PVCs bound. If not all immediate PVCs are bound, an
// UnschedulableAndUnresolvable is returned.
func (pl *StorageCapacityPrioritization) PreFilter(ctx context.Context, state *framework.CycleState, pod *v1.Pod) *framework.Status {
	// initialize state data
	state.Write(stateKey, &stateData{storageClassNames: sets.NewString()})
	return nil
}

// PreFilterExtensions returns prefilter extensions, pod add and remove.
func (pl *StorageCapacityPrioritization) PreFilterExtensions() framework.PreFilterExtensions {
	return nil
}

func (pl *StorageCapacityPrioritization) Filter(ctx context.Context, cs *framework.CycleState, pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	node := nodeInfo.Node()
	if node == nil {
		return framework.NewStatus(framework.Error, "node not found")
	}
	vbstate, err := volumebinding.GetStateData(cs)
	if err != nil {
		return framework.AsStatus(fmt.Errorf("failed to get VolumeBinding state data: %s", err.Error()))
	}
	podVolume := vbstate.GetPodVolumesByNodeName(node.GetName())
	if podVolume == nil {
		return nil
	}
	claims, err := pl.claimsByStorageClass(podVolume.DynamicProvisions)
	if err != nil {
		return framework.AsStatus(err)
	}

	reasons, err := pl.hasEnoughCapacities(claims, node)
	if err != nil {
		return framework.AsStatus(err)
	}
	if len(reasons) > 0 {
		status := framework.NewStatus(framework.UnschedulableAndUnresolvable)
		for _, reason := range reasons {
			status.AppendReason(string(reason))
		}
		return status
	}
	state, err := getStateData(cs)
	if err != nil {
		return framework.AsStatus(err)
	}
	state.Lock()
	defer state.Unlock()
	for sc := range claims {
		state.storageClassNames.Insert(sc)
	}
	return nil
}

func (pl *StorageCapacityPrioritization) PreScore(ctx context.Context, cs *framework.CycleState, pod *v1.Pod, nodes []*v1.Node) *framework.Status {
	vbstate, err := volumebinding.GetStateData(cs)
	if err != nil {
		return framework.AsStatus(fmt.Errorf("failed to get VolumeBinding state data: %s", err.Error()))
	}
	claims := vbstate.GetClaimsToBind()
	if len(claims) == 0 {
		return nil
	}
	claimsBySC, err := pl.claimsByStorageClass(claims)
	if err != nil {
		framework.AsStatus(err)
	}

	state, err := getStateData(cs)
	if err != nil {
		return framework.AsStatus(fmt.Errorf("failed to get state data: %s", err.Error()))
	}

	capacities, err := pl.csiStorageCapacityLister.List(labels.Everything())
	if err != nil && !apierrors.IsNotFound(err) {
		return framework.AsStatus(fmt.Errorf("failed to find csi storage capacities err=%v", err))
	}

	scores, err := calculateScore(nodes, state.storageClassNames.List(), capacities, claimsBySC)
	if err != nil {
		return framework.AsStatus(fmt.Errorf("failed to calcurate scores: %s", err.Error()))
	}
	state.scores = scores
	return nil
}

func (pl *StorageCapacityPrioritization) ScoreExtensions() framework.ScoreExtensions {
	return nil
}

func (pl *StorageCapacityPrioritization) Score(ctx context.Context, cs *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	state, err := getStateData(cs)
	if err != nil {
		return 0, framework.AsStatus(fmt.Errorf("failed to get state data: %s", err.Error()))
	}
	for node, score := range state.scores {
		if node == nodeName {
			return score, nil
		}
	}
	return 0, nil
}

func (pl *StorageCapacityPrioritization) claimsByStorageClass(claimsToProvision []*v1.PersistentVolumeClaim) (claimsByStorageClass, error) {
	claims := claimsByStorageClass{}
	for _, claim := range claimsToProvision {
		className := *claim.Spec.StorageClassName
		_, err := pl.classLister.Get(className)
		if err != nil {
			return nil, fmt.Errorf("failed to find storage class %q", className)
		}
		if _, ok := claims[className]; !ok {
			claims[className] = []*v1.PersistentVolumeClaim{}
		}
		claims[className] = append(claims[className], claim)
	}
	return claims, nil
}

func (pl *StorageCapacityPrioritization) hasEnoughCapacities(csc claimsByStorageClass, node *v1.Node) ([]string, error) {
	var reasons []string
	for className, cg := range csc {
		reason, err := pl.hasEnoughCapacity(node, className, cg)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			reasons = append(reasons, reason)
		}
	}
	return reasons, nil
}

func (pl *StorageCapacityPrioritization) hasEnoughCapacity(node *v1.Node, className string, cg claimGroup) (string, error) {
	class, err := pl.classLister.Get(className)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Sprintf("storage class %q is not found", className), nil
		}
		return "", fmt.Errorf("failed to find storage class %q err=%v", className, err)
	}

	driver, err := pl.csiDriverLister.Get(class.Provisioner)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to find csi driver object %q err=%v", class.Provisioner, err)
	}
	if driver.Spec.StorageCapacity == nil || !*driver.Spec.StorageCapacity {
		return "", nil
	}

	capacities, err := pl.csiStorageCapacityLister.List(labels.Everything())
	if err != nil && !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("failed to find csi storage capacities err=%v", err)
	}

	sizeInBytes, err := cg.totalRequiredCapacity()
	if err != nil {
		return "", err
	}

	for _, capacity := range capacities {
		if capacity.StorageClassName == className &&
			capacitySufficient(capacity, sizeInBytes) &&
			nodeHasAccess(node, capacity) {
			// Enough capacity found.
			return "", nil
		}
	}
	return fmt.Sprintf("there is nothing enough capacities of csi storage capacity objects. node=%q sizeInBytes=%d", node.GetName(), sizeInBytes), nil
}

func calculateScore(nodes []*v1.Node, storageClassNames []string, capacities []*v1beta1.CSIStorageCapacity, claims claimsByStorageClass) (map[string]int64, error) {
	capacityUsageMap := make(map[string]map[string]int64) // map[nodeName]map[claimName]usage
	for _, className := range storageClassNames {
		for _, node := range nodes {
			var found bool
			var capacity int64
			for _, cap := range capacities {
				if cap.StorageClassName == className && nodeHasAccess(node, cap) {
					found = true
					capacity = cap.Capacity.Value()
				}
			}
			if !found {
				continue
			}
			claimGroup, ok := claims[className]
			if !ok {
				return nil, fmt.Errorf("storage class %q is not found in claim groups", className)
			}
			request, err := claimGroup.totalRequiredCapacity()
			if err != nil {
				return nil, err
			}
			usage := float64(request) / float64(capacity)
			if usage > 1 {
				usage = 1
			}
			if capacityUsageMap[node.GetName()] == nil {
				capacityUsageMap[node.GetName()] = make(map[string]int64)
			}
			capacityUsageMap[node.GetName()][className] = int64(usage * 100)
		}
	}

	nodeScores := map[string]int64{}
	for nodeName, usageMap := range capacityUsageMap {
		nodeScores[nodeName] = 0
		var count int64
		for _, cap := range usageMap {
			nodeScores[nodeName] += cap
			count += 1
		}
		nodeScores[nodeName] = nodeScores[nodeName] / count
	}
	return nodeScores, nil
}

func capacitySufficient(capacity *storagev1beta1.CSIStorageCapacity, sizeInBytes int64) bool {
	limit := capacity.Capacity
	if capacity.MaximumVolumeSize != nil {
		// Prefer MaximumVolumeSize if available, it is more precise.
		limit = capacity.MaximumVolumeSize
	}
	return limit != nil && limit.Value() >= sizeInBytes
}

func nodeHasAccess(node *v1.Node, capacity *storagev1beta1.CSIStorageCapacity) bool {
	if capacity.NodeTopology == nil {
		// Unavailable
		return false
	}
	// Only matching by label is supported.
	selector, err := metav1.LabelSelectorAsSelector(capacity.NodeTopology)
	if err != nil {
		// This should never happen because NodeTopology must be valid.
		klog.ErrorS(err, "Unexpected error converting to a label selector", "nodeTopology", capacity.NodeTopology)
		return false
	}
	return selector.Matches(labels.Set(node.Labels))
}
