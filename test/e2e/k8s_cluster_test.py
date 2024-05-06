import pytest


@pytest.mark.first
class TestK8sCluster:
    def test_6_nodes_total(self, nodes):
        assert len(nodes.items) == 6

    def test_nodes_role_label(self, nodes):
        for node in nodes.items:
            assert node.metadata.labels["weka.io/role"] is not None

    def test_5_backend_nodes(self, backend_nodes):
        assert len(backend_nodes) == 5

    def test_backend_nodes_ready(self, backend_nodes):
        for node in backend_nodes:
            ready_condition = [
                condition
                for condition in node.status.conditions
                if condition.type == "Ready"
            ]
            assert len(ready_condition) == 1
            assert ready_condition[0].status == "True"

    def test_builder_node(self, builder_nodes):
        assert len(builder_nodes) == 1

    def test_builder_node_ready(self, builder_nodes):
        for node in builder_nodes:
            ready_condition = [
                condition
                for condition in node.status.conditions
                if condition.type == "Ready"
            ]
            assert len(ready_condition) == 1
            assert ready_condition[0].status == "True"
