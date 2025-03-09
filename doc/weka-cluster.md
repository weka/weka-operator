
Example of `weka status`
```
       cluster: cluster-name (550c68b6-8f64-4073-8086-efc3ba69207e)
        status: OK (17/21 backend containers UP, 36 drives UP)
    protection: 3+2 (Fully protected)
     hot spare: 0 failure domains
 drive storage: 210.59 TiB total, 209.00 TiB unprovisioned
         cloud: connected
       license: Unlicensed

     io status: STARTED 19 days ago (29/33 io-nodes UP, 156 Buckets UP)
    link layer: Ethernet
       clients: 4 connected
         reads: 0 B/s (0 IO/s)
        writes: 0 B/s (0 IO/s)
    operations: 6 ops/s
        alerts: 52 active alerts, use `weka alerts` to list them
```
Statuses such as OK, REDISTRIBUTING, PARTIALLY_PROTECTED, REBUILDING considered healthy
there is also variant of which provides json output, `weka status --json`

Often-used commands:

Find leadership processes, and weka container name via it:
`weka cluster processes -o id,container -l`
To find a leader (one amongst leadership)
`weka cluster processes -o id,container -L`

Container name from within weka can be correlated to k8s name container by wekacontainer.spec.name
For example, following command will find all weka containers in infra namespace that belong to cluster with id b0dbe115-3d64-465a-ba5c-d3f50c2de60e
In addition it filters only drive container, as drive containers can be part of leadership
```kubectl get -n NAMESPACE wekacontainer -o custom-columns=NAME:.metadata.name,SPEC_NAME:.spec.name -l weka.io/cluster-id=b0dbe115-3d64-465a-ba5c-d3f50c2de60e,weka.io/mode=drive```
Output provides information as 
```
NAME                                                                          SPEC_NAME
test-raft9resilience-4pblijjlbfj-drive-07d2d65f-2d75-4791-a737-779ac317a17a   drivex07d2d65fx2d75x4791xa737x779ac317a17a
test-raft9resilience-4pblijjlbfj-drive-0dbc4b5e-115a-433d-81bd-e4fabbd43298   drivex0dbc4b5ex115ax433dx81bdxe4fabbd43298
```

Use `--no-headers` for easier parsing, use combination of grep for values you search and awk for outputting first columns to get the pod name, for example
```kubectl get -n infra wekacontainer -o custom-columns=NAME:.metadata.name,SPEC_NAME:.spec.name -l weka.io/cluster-id=550c68b6-8f64-4073-8086-efc3ba69207e,weka.io/mode=drive --no-headers | grep -e drivex07d2d65fx2d75x4791xa737x779ac317a17a -e drivex0dbc4b5ex115ax433dx81bdxe4fabbd43298 | awk '{print $1}'```
Grepping this command by expected weka container name allows to find the pod name and then exec into it to run weka commands
