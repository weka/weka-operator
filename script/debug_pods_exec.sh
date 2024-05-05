#!/bin/bash

# Define the DaemonSet base name
daemonset_base_name="debug-pod"

# Get all pods along with their node names using a label selector or a wildcard in the name
pods_info=$(kubectl get pods --selector=name=$daemonset_base_name --output=jsonpath='{range .items[*]}{.metadata.name}{" on "}{.spec.nodeName}{"\n"}{end}')

# Loop through each line of pod info and execute the command
while IFS= read -r line; do
    pod_name=$(echo $line | cut -d' ' -f1)
    node_name=$(echo $line | cut -d' ' -f3)
    echo "Output from pod $pod_name on node $node_name:"
    kubectl exec $pod_name -- sh -c "$@"
    echo "-----------------------------"
done <<< "$pods_info"