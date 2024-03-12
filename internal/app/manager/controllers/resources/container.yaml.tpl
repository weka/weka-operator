---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: "{{ .Name }}"
  namespace: "{{ .Namespace }}"
  labels:
    app: "{{ .Name }}"
spec:
  replicas: 1
  selector:
    matchLabels:
      app: "{{ .Name }}"
  strategy:
    rollingUpdate:
      maxSurge: 25%
      maxUnavailable: 25%
    type: RollingUpdate
  template:
    metadata:
      labels:
        app: "{{ .Name }}"
    spec:
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  - key: kubernetes.io/hostname
                    operator: In
                    values:
                      - "{{ .NodeName }}"
      # ...
      imagePullSecrets:
        - name: "{{ .ImagePullSecretName }}"
      containers:
        - name: "{{ .Name }}"
          image: "{{ .Image }}"
          imagePullPolicy: Always
          command:
            - /opt/startup-script.sh

          securityContext:
            runAsNontRoot: false
            privileged: true
            runAsUser: 0
            capabilities:
              add:
                - ALL
            seccompProfile:
              type: Unconfined

          volumeMounts:
            - name: host-dev
              mountPath: /dev
            - name: "hugepage-2mi"
              mountPath: /mnt/huge
              readOnly: false
            - name: supervisord-config
              mountPath: /etc/supervisord
            - name: startup-script
              mountPath: /opt

          resources:
            limits:
              cpu: "2"
              hugepages-2Mi: 2Gi
              memory: "8Gi"
            requests:
              cpu: "2"
              hugepages-2Mi: 2Gi
              memory: "8Gi"

          env:
            - name: "WEKA_VERSION"
              value: "{{ .WekaVersion }}"
            - name: "BACKEND_PRIVATE_IP"
              value: "{{ .BackendIP }}"
            - name: "MANAGEMENT_IPS"
              valueFrom:
                fieldRef:
                  fieldPath: status.hostIP
            - name: "MANAGEMENT_PORT"
              value: "{{ .ManagementPort }}"
            - name: "BACKEND_NET"
              value: "{{ .InterfaceName }}"

          ports:
            - containerPort: 14000
            - containerPort: 14100

      volumes:
        - name: host-dev
          hostPath:
            path: /dev
        - name: hugepage-2mi
          emptyDir:
            medium: HugePages
        - name: supervisord-config
          configMap:
            name: "supervisord-conf"
            key: supervisord.conf
            path: supervisord.conf
        - name: startup-script
          configMap:
            name: "startup-script"
            key: startup-script.sh
            path: startup-script.sh
            defaultMode: 0744
