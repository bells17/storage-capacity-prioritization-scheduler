// +build !ignore_autogenerated

// Code generated by deepcopy-gen. DO NOT EDIT.

package config

import (
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *StorageCapacityPrioritizationArgs) DeepCopyInto(out *StorageCapacityPrioritizationArgs) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new StorageCapacityPrioritizationArgs.
func (in *StorageCapacityPrioritizationArgs) DeepCopy() *StorageCapacityPrioritizationArgs {
	if in == nil {
		return nil
	}
	out := new(StorageCapacityPrioritizationArgs)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *StorageCapacityPrioritizationArgs) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}