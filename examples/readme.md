

API Test:
- Create cluster: `curl -v -H"Content-Type: application/json" -d @examples/api_cluster_create.json http://localhost:8082/clusters/default/testapi`
- Get cluster: `curl -v http://localhost:8082/clusters/default/testapi`

Another cluster:
- Create cluster: `curl -v -H"Content-Type: application/json" -d @examples/api_cluster_create.json http://localhost:8082/clusters/default/testapi2`
- Get cluster: `curl -v http://localhost:8082/clusters/default/testapi2`
- Get cluster status: `curl -v http://localhost:8082/clusters/default/testapi2`
- curl -v http://localhost:8082/clusters/default/testapi2/status | jq '.status'
