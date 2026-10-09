import importlib.util
import os
import sys
import shlex
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

path = Path(__file__).resolve().parents[1] / 'skills/yoooclaw-media-generate/scripts/execution_guard.py'
sys.path.insert(0, str(path.parent))
spec = importlib.util.spec_from_file_location('guard', path)
g = importlib.util.module_from_spec(spec)
spec.loader.exec_module(g)

class GuardTests(unittest.TestCase):
    def pre(self, **kwargs):
        return g.pre_tool({'session_id':'test', 'tool_name':'Bash', 'tool_input':{'command':'python3 '+shlex.quote(str(path.with_name('video_generate.py')))+' generate --resolution 480P --seconds 2 --prompt hello --confirmed', **kwargs}})
    def test_background_denied(self):
        self.assertEqual(self.pre(run_in_background=True,timeout=1200000)['hookSpecificOutput']['permissionDecision'],'deny')
    def test_timeout_denied(self):
        self.assertEqual(self.pre()['hookSpecificOutput']['permissionDecision'],'deny')
    def test_foreground_preserves_command(self):
        r=self.pre(timeout=1200000)['hookSpecificOutput']
        self.assertNotIn('permissionDecision',r)
        self.assertIn('export YOOOCLAW_MEDIA_SESSION=test;',r['updatedInput']['command'])
    def test_unrelated_allowed(self):
        self.assertIsNone(g.pre_tool({'tool_name':'Bash','tool_input':{'command':'python3 video_generate.py estimate --seconds 5'}}))
    def test_positive_command_matching(self):
        script=shlex.quote(str(path.with_name('video_generate.py')))
        valid='python3 '+script+' generate --resolution 480P --seconds 2 --prompt hello --confirmed'
        for command in [valid, valid+' &', 'nohup '+valid, 'env TEST=1 '+valid]:
            self.assertEqual(self.pre(command=command)['hookSpecificOutput']['permissionDecision'],'deny')
        for command in ['pwd', 'echo '+valid, 'printf "%s" '+shlex.quote(valid),
                        'python3 /tmp/unrelated/video_generate.py generate --resolution 480P --seconds 2 --prompt hello',
                        'python3 '+script+' generate --help', 'python3 '+script+' --help',
                        'python3 '+script+' generate', 'python3 -c '+shlex.quote('print('+repr(valid)+')')]:
            self.assertIsNone(self.pre(command=command),command)
        self.assertIsNone(self.pre(command='python3 '+script+' estimate --resolution 480P --seconds 2'))
        r=self.pre(command=valid.replace('--prompt hello','--prompt "&"'),timeout=1200000)
        self.assertNotIn('permissionDecision',r['hookSpecificOutput'])
        relative='python3 video_generate.py query existing-task'
        self.assertFalse(g.media_command(relative,str(path.parent)))
        self.assertIsNone(g.media_command(relative,'/tmp'))

    def test_states_and_sessions(self):
        with tempfile.TemporaryDirectory() as home, patch.dict(os.environ,{'HOME':home,'YOOOCLAW_MEDIA_SESSION':'test'}):
            state=g.TaskState(SimpleNamespace(command='query',task_id='test-task',wait=1200))
            self.assertEqual(g.stop({'session_id':'test'})['decision'],'block')
            self.assertIsNone(g.stop({'session_id':'other'}))
            state.update(phase='ended')
            self.assertIsNone(g.stop({'session_id':'test'}))
    def test_active_keeps_blocking(self):
        with tempfile.TemporaryDirectory() as home, patch.dict(os.environ,{'HOME':home,'YOOOCLAW_MEDIA_SESSION':'test'}):
            state=g.TaskState(SimpleNamespace(command='query',task_id='test-task',wait=1200))
            for _ in range(3):
                self.assertEqual(g.stop({'session_id':'test','stop_hook_active':True})['decision'],'block')
            state.update(status='SUCCEEDED',phase='ended',exit_code=0)
            self.assertIsNone(g.stop({'session_id':'test','stop_hook_active':True}))
    def test_interrupted_requires_query(self):
        with tempfile.TemporaryDirectory() as home, patch.dict(os.environ,{'HOME':home,'YOOOCLAW_MEDIA_SESSION':'test'}):
            state=g.TaskState(SimpleNamespace(command='query',task_id='test-task',wait=1200))
            with patch.object(g.os,'kill',side_effect=ProcessLookupError):
                result=g.stop({'session_id':'test','stop_hook_active':True})
            self.assertEqual(result['decision'],'block')
            self.assertIn('query',result['reason'])
            query=g.TaskState(SimpleNamespace(command='query',task_id='test-task',wait=1200))
            query.update(status='SUCCEEDED',phase='ended',exit_code=0)
            self.assertIsNone(g.stop({'session_id':'test'}))
    def test_timeout_and_error_allow_reporting(self):
        with tempfile.TemporaryDirectory() as home, patch.dict(os.environ,{'HOME':home,'YOOOCLAW_MEDIA_SESSION':'test'}):
            state=g.TaskState(SimpleNamespace(command='query',task_id='test-task',wait=1200))
            state.update(deadline=0)
            self.assertNotIn('decision',g.stop({'session_id':'test'}))
            state.update(phase='ended',exit_code=1)
            self.assertIsNone(g.stop({'session_id':'test'}))

if __name__=='__main__':unittest.main()
