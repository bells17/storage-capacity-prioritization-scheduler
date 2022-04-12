# storage-capacity-prioritization-scheduler

This application is a custom scheduler for kubernetes.
It has a built-in StorageCapacityPrioritization plugin that filters/prioritizes nodes using the Capacity field of the StorageCapacity resource.
Since this plugin uses StateData of VolumeBinding plugin, some code of VolumeBinding plugin is patched so that StateData can be accessed.
It may be best to incorporate the processing of this plugin as part of the VolumeBinding plugin, but since it is a sample implementation, I implemented it as a new Scheduling Framework plugin.

## init

```
$ make setup
$ make init
```

## test

```
$ make test
```
