"""Run with make test-runtime."""
import ast
import asyncio
import logging
import os
import tempfile
from functools import lru_cache
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock


SOURCE = Path(__file__).resolve().parents[2] / 'charts/weka-operator/resources/weka_runtime.py'
FUNCTIONS = {
    'parse_cpu_allowed_list', 'expand_ranges', 'get_container_cpu_allocation',
    'get_process_args', 'is_wekanode', 'is_ionode', 'get_remaining_cores',
    'set_process_affinity', 'manage_cpu_affinities', 'periodic_cpu_affinity_management',
    'is_host_pid_namespace', 'get_agent_cmd', 'get_pinned_agent_cmd', 'get_support_cpus',
    'install_exec_shell_pinning', 'publish_support_cpus', 'setup_early_cpu_affinity',
}


class RuntimeAffinityTest(unittest.TestCase):
    def setUp(self):
        # Load production functions without executing the pod-only startup code.
        tree = ast.parse(SOURCE.read_text())
        tree.body = [n for n in tree.body if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef))
                     and n.name in FUNCTIONS
                     or isinstance(n, ast.Assign) and any(getattr(t, 'id', None) == 'MODE_CORES_FLAG' for t in n.targets)]
        self.os = Mock()
        self.os.path = os.path
        self.ns = dict(os=self.os, logging=logging, lru_cache=lru_cache,
                       asyncio=asyncio, shutil=SimpleNamespace(which=lambda name: '/usr/bin/' + name), MODE='drive', AGENT_PORT=14000, NUM_CORES=1, NON_DATAPATH_CORE_IDS='auto')
        exec(compile(tree, str(SOURCE), 'exec'), self.ns)

    def test_allocation_survives_runtime_narrowing(self):
        self.ns['parse_cpu_allowed_list'] = Mock(side_effect=[[5, 6], [6]])
        self.assertEqual(self.ns['get_container_cpu_allocation'](), (5, 6))
        self.assertEqual(self.ns['get_container_cpu_allocation'](), (5, 6))
        self.ns['parse_cpu_allowed_list'].assert_called_once()

    def setup_cores(self):
        self.ns['get_container_cpu_allocation'] = lambda: (5, 6, 7, 8)
        self.ns['find_full_cores'] = lambda n: [5]
        self.ns['read_siblings_list'] = lambda c: [5, 7] if c in [5, 7] else [c]
        self.os.listdir.return_value = []

    def test_configured_core_and_sibling_stay_reserved_without_process(self):
        self.setup_cores()
        self.assertEqual(self.ns['get_remaining_cores'](), {6, 8})

    def test_explicit_support_cores_intersect_safe_allocation(self):
        self.setup_cores()
        self.ns['NON_DATAPATH_CORE_IDS'] = '5-7,99'
        self.assertEqual(self.ns['get_remaining_cores'](), {6})

    def test_inherited_ionode_mask_does_not_shrink_support_cores(self):
        self.setup_cores()
        self.os.listdir.return_value = ['123']
        self.ns['get_process_args'] = lambda pid: ['/weka/wekanode', '--slot', '1']
        self.os.sched_getaffinity.return_value = {6, 8}
        self.assertEqual(self.ns['get_remaining_cores'](), {6, 8})

    def test_correct_leader_does_not_hide_unpinned_thread(self):
        self.os.listdir.return_value = ['10', '11', '12']
        self.os.sched_getaffinity.side_effect = [{6}, {5, 6}, ProcessLookupError()]
        self.assertEqual(self.ns['set_process_affinity'](10, {6}), 1)
        self.os.sched_setaffinity.assert_called_once_with(11, {6})

    def test_slot_parsing(self):
        for args, expected in [(['--slot', '0'], False), (['--slot=0'], False),
                               (['--slot', '1'], True), (['--slot=10'], True), ([], True)]:
            self.assertEqual(self.ns['is_ionode'](['/weka/wekanode'] + args), expected)
        self.assertFalse(self.ns['is_ionode'](['sh', '-c', '/weka/wekanode --slot 1']))

    def run_management(self, native):
        self.ns['get_remaining_cores'] = lambda: {6}
        async def flags():
            return SimpleNamespace(weka_manages_non_ionode_affinity=native)
        self.ns['get_feature_flags'] = flags
        self.os.getpid.return_value = 1
        self.os.listdir.return_value = ['1', '2', '3', '4', '5', '6']
        commands = {'2': ['/usr/bin/weka', '--agent'], '3': ['/usr/sbin/syslog-ng'],
                    '4': ['/usr/sbin/logrotate'], '5': ['/weka/wekanode', '--slot', '1'],
                    '6': ['/weka/wekanode', '--slot', '0']}
        self.ns['get_process_args'] = commands.__getitem__
        self.ns['set_process_affinity'] = Mock()
        asyncio.run(self.ns['manage_cpu_affinities']())
        return [call.args[0] for call in self.ns['set_process_affinity'].call_args_list]

    def test_native_feature_still_pins_runtime_agent_and_logging(self):
        self.assertEqual(self.run_management(True), [1, 2, 3, 4])
        self.os.sched_setaffinity.assert_called_once_with(0, {6})

    def test_fallback_also_pins_management_wekanode(self):
        self.assertEqual(self.run_management(False), [1, 2, 3, 4, 6])

    def test_periodic_task_repairs_immediately_then_waits(self):
        events = []
        self.ns['exiting'] = False
        async def manage():
            events.append('repair')
        async def sleep(seconds):
            events.append(seconds)
            self.ns['exiting'] = True
        self.ns['manage_cpu_affinities'] = manage
        self.ns['asyncio'] = SimpleNamespace(sleep=sleep)
        self.os.readlink.return_value = 'mnt:[1]'
        asyncio.run(self.ns['periodic_cpu_affinity_management']())
        self.assertEqual(events, ['repair', 60])

    def test_host_pid_pod_skips_affinity_management(self):
        self.ns['exiting'] = False
        self.ns['manage_cpu_affinities'] = Mock()
        self.os.readlink.side_effect = lambda path: 'mnt:[1]' if path == '/proc/1/ns/mnt' else 'mnt:[2]'
        asyncio.run(self.ns['periodic_cpu_affinity_management']())
        self.ns['manage_cpu_affinities'].assert_not_called()

    def setup_agent(self, cores, host_pid=False):
        self.ns['get_remaining_cores'] = lambda: cores
        self.ns['is_host_pid_namespace'] = lambda: host_pid

    def test_agent_pinned_to_sorted_support_mask(self):
        self.setup_agent({8, 6})
        cmd = self.ns['get_pinned_agent_cmd'](self.ns['get_support_cpus']())
        self.assertTrue(cmd.startswith('exec taskset -c 6,8 /usr/bin/weka --agent'))

    def test_no_support_cpus_on_host_pid(self):
        self.setup_agent({6}, host_pid=True)
        self.assertIsNone(self.ns['get_support_cpus']())

    def test_no_support_cpus_on_empty_mask(self):
        self.setup_agent(set())
        self.assertIsNone(self.ns['get_support_cpus']())

    def test_no_support_cpus_for_modes_without_weka_cores(self):
        self.setup_agent({6})
        self.ns['MODE'] = 'envoy'
        self.assertIsNone(self.ns['get_support_cpus']())

    def test_support_cpus_for_every_mode_with_weka_cores(self):
        self.setup_agent({6})
        for mode in ['drive', 'compute', 'client', 's3', 'nfs', 'smbw', 'data-services']:
            self.ns['MODE'] = mode
            self.assertEqual(self.ns['get_support_cpus'](), '6', mode)

    def test_agent_unpinned_without_support_cpus(self):
        self.assertEqual(self.ns['get_pinned_agent_cmd'](None), self.ns['get_agent_cmd']())

    def test_exec_shell_pinning_installs_once_and_reads_published_mask(self):
        with tempfile.TemporaryDirectory() as tmp:
            bashrc = os.path.join(tmp, 'bashrc')
            Path(bashrc).write_text('# existing\n')
            self.ns.update(os=os, EXEC_SHELL_DIR=os.path.join(tmp, 'rt'),
                           EXEC_SHELL_CPUS_PATH=os.path.join(tmp, 'rt', 'cpus'),
                           EXEC_SHELL_PIN_SCRIPT_PATH=os.path.join(tmp, 'rt', 'pin.sh'),
                           EXEC_SHELL_PIN_SCRIPT='script', EXEC_SHELL_BASHRC_LINE='. pin.sh')
            self.ns['install_exec_shell_pinning'](bashrc)
            self.ns['install_exec_shell_pinning'](bashrc)
            self.assertFalse(os.path.exists(self.ns['EXEC_SHELL_CPUS_PATH']))
            self.ns['publish_support_cpus']('22')
            self.assertEqual(Path(self.ns['EXEC_SHELL_CPUS_PATH']).read_text(), '22\n')
            self.assertEqual(Path(self.ns['EXEC_SHELL_PIN_SCRIPT_PATH']).read_text(), 'script')
            self.assertEqual(Path(bashrc).read_text().count('. pin.sh'), 1)

    def test_exec_shell_pinning_creates_missing_bashrc(self):
        with tempfile.TemporaryDirectory() as tmp:
            bashrc = os.path.join(tmp, 'bashrc')
            self.ns.update(os=os, EXEC_SHELL_DIR=os.path.join(tmp, 'rt'),
                           EXEC_SHELL_PIN_SCRIPT_PATH=os.path.join(tmp, 'rt', 'pin.sh'),
                           EXEC_SHELL_PIN_SCRIPT='script', EXEC_SHELL_BASHRC_LINE='. pin.sh')
            self.ns['install_exec_shell_pinning'](bashrc)
            self.assertEqual(Path(bashrc).read_text().count('. pin.sh'), 1)

    def test_early_affinity_failure_is_non_fatal(self):
        self.ns['install_exec_shell_pinning'] = Mock(side_effect=PermissionError('read-only'))
        self.assertIsNone(self.ns['setup_early_cpu_affinity']())

    def test_early_affinity_pins_runtime_and_publishes_mask(self):
        self.ns.update(install_exec_shell_pinning=Mock(), get_support_cpus=lambda: '6,8',
                       publish_support_cpus=Mock(), set_process_affinity=Mock())
        self.os.getpid.return_value = 1
        self.assertEqual(self.ns['setup_early_cpu_affinity'](), '6,8')
        self.ns['publish_support_cpus'].assert_called_once_with('6,8')
        self.ns['set_process_affinity'].assert_called_once_with(1, {6, 8})

    def test_early_affinity_skips_modes_without_weka_cores(self):
        self.ns['MODE'] = 'envoy'
        self.ns['install_exec_shell_pinning'] = Mock()
        self.assertIsNone(self.ns['setup_early_cpu_affinity']())
        self.ns['install_exec_shell_pinning'].assert_not_called()

    def test_empty_target_does_not_modify_affinity(self):
        self.ns['get_remaining_cores'] = lambda: set()
        asyncio.run(self.ns['manage_cpu_affinities']())
        self.os.sched_setaffinity.assert_not_called()


if __name__ == '__main__':
    unittest.main()
