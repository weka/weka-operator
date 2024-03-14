# weka-operator

*Caution* This is still a prototype/demonstration

## Branches

Development branch -> `main`
Omniva 0.11 Hotfixes -> `v0.11`

The `main` branch is diverging from Omniva's installation in order to implement April 2 requirements.
Bugfixes must be made against the `v0.11` branch until Omniva can be upgraded.

## Description

This is a prototype of the Operator.
It defines a CRD, and if an instance of this CRD is deployed, then the Operator will create a BusyBox deployment.

## Getting Started

I use `asdf` to install the prerequisite development tools.
These are listed in `.tool-versions`.
You should be able to bulk-install these using `asdf install` in the project root.

### Creating a Development Cluster

You’ll need a Kubernetes cluster to run against.
I've included KIND from `asdf` so you should now have it installed.
Kind depends on docker desktop.

There is a script in the `script` folder that will provision a local cluster with an image registry.
Run `./script/kind-with-registry.sh`

### Configure Networking

The above script will create a docker network, but there will be some inconsistency in host names between that network and your laptop's host network.
To work around these inconsistencies, I added this to my `/etc/hosts` file:

```plaintext
127.0.0.1       kind-registry
```

### Running on the cluster

1. Install Instances of Custom Resources:

```sh
kubectl apply -f config/samples/
```

2. Build and push your image to the local registry

```sh
make docker-build docker-push 
```

3. Deploy the controller to the cluster with the image specified by `IMG`:

```sh
make deploy
```

### Uninstall CRDs

To delete the CRDs from the cluster:

```sh
make uninstall
```

### Undeploy controller

UnDeploy the controller from the cluster:

```sh
make undeploy
```

### How it works

This project aims to follow the Kubernetes [Operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/).

It uses [Controllers](https://kubernetes.io/docs/concepts/architecture/controller/),
which provide a reconcile function responsible for synchronizing resources until the desired state is reached on the cluster.
The `Reconcile` method in `client_controller.go` implements the management behavior.
The CRD definition is generated from structs in `client_types.go`

### Test It Out

#### Install the Operator

1. Install the CRDs into the cluster:

```sh
make install
```

2. Run your controller (this will run in the foreground, so switch to a new terminal if you want to leave it running):

```sh
make run
```

**NOTE:** You can also run this in one step by running: `make install run`

#### Verifying

##### Log Output
The output to `make run` will include log messages from the `Reconcile` loop.
Look for "Reconciling Client" and "Finished Reconciling Client".
These are the start and end events of the Reconcile loop.

##### K9s

To verify in k9s, connect to the cluster in k9s.
(Cluster selection should be automatic)

View the CRD instances using `:weka.weka.io/v1alpha1/clients`
The Status fields are provided by the operator.

To view the created Pod: `:pod`
And select the pod with `client-sample` in its name.
The `client-sample` name is derived from the `metadata.name` field in the CR.

### Modifying the API definitions

If you are editing the API definitions, generate the manifests such as CRs or CRDs using:

```sh
make manifests
```

**NOTE:** Run `make --help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)
