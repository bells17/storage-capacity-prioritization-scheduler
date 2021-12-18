package storagecapacityprioritization

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	storagev1beta1 "k8s.io/api/storage/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	pvutil "k8s.io/kubernetes/pkg/controller/volume/persistentvolume/util"
	"k8s.io/utils/pointer"
)

type nodeBuilder struct {
	*v1.Node
}

func makeNode(name string) nodeBuilder {
	return nodeBuilder{Node: &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				v1.LabelHostname: name,
			},
		},
	}}
}

func (nb nodeBuilder) withLabel(key, value string) nodeBuilder {
	if nb.Node.ObjectMeta.Labels == nil {
		nb.Node.ObjectMeta.Labels = map[string]string{}
	}
	nb.Node.ObjectMeta.Labels[key] = value
	return nb
}

type pvBuilder struct {
	*v1.PersistentVolume
}

func makePV(name, className string) pvBuilder {
	return pvBuilder{PersistentVolume: &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.PersistentVolumeSpec{
			StorageClassName: className,
		},
	}}
}

func (pvb pvBuilder) withNodeAffinity(keyValues map[string][]string) pvBuilder {
	matchExpressions := make([]v1.NodeSelectorRequirement, 0)
	for key, values := range keyValues {
		matchExpressions = append(matchExpressions, v1.NodeSelectorRequirement{
			Key:      key,
			Operator: v1.NodeSelectorOpIn,
			Values:   values,
		})
	}
	pvb.PersistentVolume.Spec.NodeAffinity = &v1.VolumeNodeAffinity{
		Required: &v1.NodeSelector{
			NodeSelectorTerms: []v1.NodeSelectorTerm{
				{
					MatchExpressions: matchExpressions,
				},
			},
		},
	}
	return pvb
}

func (pvb pvBuilder) withVersion(version string) pvBuilder {
	pvb.PersistentVolume.ObjectMeta.ResourceVersion = version
	return pvb
}

func (pvb pvBuilder) withCapacity(capacity resource.Quantity) pvBuilder {
	pvb.PersistentVolume.Spec.Capacity = v1.ResourceList{
		v1.ResourceName(v1.ResourceStorage): capacity,
	}
	return pvb
}

func (pvb pvBuilder) withPhase(phase v1.PersistentVolumePhase) pvBuilder {
	pvb.PersistentVolume.Status = v1.PersistentVolumeStatus{
		Phase: phase,
	}
	return pvb
}

type pvcBuilder struct {
	*v1.PersistentVolumeClaim
}

func makePVC(name string, storageClassName string) pvcBuilder {
	return pvcBuilder{PersistentVolumeClaim: &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: v1.NamespaceDefault,
		},
		Spec: v1.PersistentVolumeClaimSpec{
			StorageClassName: pointer.StringPtr(storageClassName),
		},
	}}
}

func (pvcb pvcBuilder) withBoundPV(pvName string) pvcBuilder {
	pvcb.PersistentVolumeClaim.Spec.VolumeName = pvName
	metav1.SetMetaDataAnnotation(&pvcb.PersistentVolumeClaim.ObjectMeta, pvutil.AnnBindCompleted, "true")
	return pvcb
}

func (pvcb pvcBuilder) withRequestStorage(request resource.Quantity) pvcBuilder {
	pvcb.PersistentVolumeClaim.Spec.Resources = v1.ResourceRequirements{
		Requests: v1.ResourceList{
			v1.ResourceName(v1.ResourceStorage): request,
		},
	}
	return pvcb
}

func (pvcb pvcBuilder) withPhase(phase v1.PersistentVolumeClaimPhase) pvcBuilder {
	pvcb.PersistentVolumeClaim.Status = v1.PersistentVolumeClaimStatus{
		Phase: phase,
	}
	return pvcb
}

type podBuilder struct {
	*v1.Pod
}

func makePod(name string) podBuilder {
	pb := podBuilder{Pod: &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: v1.NamespaceDefault,
		},
	}}
	pb.Pod.Spec.Volumes = make([]v1.Volume, 0)
	return pb
}

func (pb podBuilder) withNodeName(name string) podBuilder {
	pb.Pod.Spec.NodeName = name
	return pb
}

func (pb podBuilder) withNamespace(name string) podBuilder {
	pb.Pod.ObjectMeta.Namespace = name
	return pb
}

func (pb podBuilder) withPVCVolume(pvcName, name string) podBuilder {
	pb.Pod.Spec.Volumes = append(pb.Pod.Spec.Volumes, v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
			},
		},
	})
	return pb
}

func (pb podBuilder) withPVCSVolume(pvcs []*v1.PersistentVolumeClaim) podBuilder {
	for i, pvc := range pvcs {
		pb.withPVCVolume(pvc.Name, fmt.Sprintf("vol%v", i))
	}
	return pb
}

func (pb podBuilder) withEmptyDirVolume() podBuilder {
	pb.Pod.Spec.Volumes = append(pb.Pod.Spec.Volumes, v1.Volume{
		VolumeSource: v1.VolumeSource{
			EmptyDir: &v1.EmptyDirVolumeSource{},
		},
	})
	return pb
}

func (pb podBuilder) withGenericEphemeralVolume(name string) podBuilder {
	pb.Pod.Spec.Volumes = append(pb.Pod.Spec.Volumes, v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			Ephemeral: &v1.EphemeralVolumeSource{},
		},
	})
	return pb
}

type cscBuilder struct {
	*storagev1beta1.CSIStorageCapacity
}

func makeCSC(name string, storageClassName string) cscBuilder {
	return cscBuilder{CSIStorageCapacity: &storagev1beta1.CSIStorageCapacity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("csisc-%s", name),
			Namespace: v1.NamespaceDefault,
		},
		StorageClassName: storageClassName,
	}}
}

func (csc cscBuilder) withCapacity(capacity resource.Quantity) cscBuilder {
	csc.Capacity = &capacity
	return csc
}

func (csc cscBuilder) withTopology(ls labels.Set) cscBuilder {
	csc.NodeTopology = metav1.SetAsLabelSelector(ls)
	return csc
}
