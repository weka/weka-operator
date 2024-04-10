# CSI plugin install (AWS)

# lots of this should move to TF
# we probably should take one step back and look into full EKS modules and not just low level resources of provider
# while it is beneficial to use low level, purpose in this project is to have quick and simple scratch setup, so module should be OK

AWS_PROFILE=devkube aws iam create-policy --policy-name Amazon_EBS_CSI_Driver --policy-document file://ebs-csi-policy.json
AWS_PROFILE=devkube aws iam create-role --role-name AmazonEBS_CSI_Driver_Role --assume-role-policy-document file://trust-policy.json
AWS_PROFILE=devkube aws iam attach-role-policy --role-name AmazonEBS_CSI_Driver_Role --policy-arn arn:aws:iam::381492135989:policy/Amazon_EBS_CSI_Driver

#register provider
THUMBPRINT=$(echo | openssl s_client -servername oidc.eks.eu-west-1.amazonaws.com -showcerts -connect oidc.eks.eu-west-1.amazonaws.com:443 2>/dev/null | openssl x509 -fingerprint -noout | cut -d "=" -f2 | sed 's/^.*=//;s/://g')
echo $THUMBPRINT

AWS_PROFILE=devkube aws iam create-open-id-connect-provider --url "https://oidc.eks.eu-west-1.amazonaws.com/id/A5C5EF1FFC3CD1362C1D235D84BE516F" --client-id-list sts.amazonaws.com --thumbprint-list "$THUMBPRINT"

#
helm repo add aws-ebs-csi-driver https://kubernetes-sigs.github.io/aws-ebs-csi-driver/
helm repo update
helm upgrade --install aws-ebs-csi-driver \
    --namespace kube-system \
    aws-ebs-csi-driver/aws-ebs-csi-driver --set controller.serviceAccount.autoMountServiceAccountToken=true

kubectl annotate serviceaccount ebs-csi-controller-sa \
  -n kube-system \
  eks.amazonaws.com/role-arn=arn:aws:iam::381492135989:role/AmazonEBS_CSI_Driver_Role


# signoz

helm repo add signoz https://charts.signoz.io;
helm repo update;

helm upgrade --install signoz signoz/signoz -n signoz --create-namespace --set k8s-infra.otelAgent.enabled=false --set k8s-infra.enabled=false
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.12.0/cert-manager.yaml;
helm upgrade --install signoz-k8s-infra signoz/k8s-infra -n signoz --set otelCollectorEndpoint=signoz-otel-collector.signoz.svc.cluster.local:4317


# uninstall
# helm uninstall signoz -n signoz
# helm uninstall signoz-k8s-infra -n signoz