"""Execute the manual caller's write commands with fakes, without Docker/network writes."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import yaml

ROOT = Path(__file__).resolve().parents[2]


class ImageRehearsal(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.path = ROOT / '.github/workflows/image-rehearsal.yml'
        cls.workflow = yaml.safe_load(cls.path.read_text())
        cls.repo = 'opflex' if cls.workflow['name'].startswith('OpFlex') else 'aci-containers'
        cls.job = cls.workflow['jobs']['build-and-publish']
        cls.steps = cls.job['steps']

    def step(self, name):
        return next(step for step in self.steps if step['name'] == name)

    def run_script(self, script, environment=None, binaries=None, metadata=False):
        with tempfile.TemporaryDirectory() as directory:
            temp = Path(directory)
            for binary, body in (binaries or {}).items():
                path = temp / binary
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('#!/bin/bash\nset -eu\n' + body)
                path.chmod(0o755)
            artifacts = temp / 'ci-artifacts'
            artifacts.mkdir()
            if metadata:
                (artifacts / 'image-metadata.jsonl').write_text('{}\n')
            env = {**os.environ, 'PATH': directory + os.pathsep + os.environ['PATH'],
                'RUNNER_TEMP': directory, 'CI_ARTIFACT_DIR': str(artifacts), 'DOCKER_CONFIG': str(temp / 'docker-config'),
                'GITHUB_OUTPUT': str(temp / 'output'), 'GITHUB_STEP_SUMMARY': str(temp / 'summary'),
                'CICD_DIR': str(temp / 'cicd'), 'TRAVIS_TAG': '1.2.3.4',
                'TRAVIS_COMMIT': 'a'*40, 'CICD_COMMIT': 'b'*40,
                'REVIEWED_SOURCE_SHA': 'a'*40, 'REVIEWED_CICD_SHA': 'b'*40,
                'GITHUB_REPOSITORY': f'anmol372/{self.repo}', 'GITHUB_RUN_ID': '100',
                'GITHUB_RUN_NUMBER': '20', 'GITHUB_RUN_ATTEMPT': '1', 'UPSTREAM_ID': 'a378a7d',
                'TRAVIS_JOB_WEB_URL': 'https://github.com/example/actions/runs/100',
                'SELECTED_SOURCE_REF': 'reviewed/source', 'IMAGE_BUILD_OUTCOME': 'success',
                'FIXTURE_ARGS': str(temp / 'args'), **(environment or {})}
            result = subprocess.run(['bash', '-c', script], cwd=temp, env=env, text=True, capture_output=True)
            files = {str(path.relative_to(temp)): path.read_text() for path in temp.rglob('*') if path.is_file()}
            return result, files

    def test_dispatch_has_fixed_host_and_execution_branch_and_no_unit_job_change(self):
        trigger = self.workflow.get('on', self.workflow.get(True))
        self.assertEqual(set(trigger), {'workflow_dispatch'})
        resolve = self.workflow['jobs']['resolve']
        self.assertIn(f"github.repository == 'anmol372/{self.repo}'", resolve['if'])
        self.assertIn("github.ref == 'refs/heads/anmrasto/gha-rehearsal-20261119'", resolve['if'])
        self.assertNotIn('secrets.', str(resolve))
        self.assertNotIn('environment', resolve)
        self.assertEqual(self.job['environment'], 'gha-rehearsal')
        for name in ['QUAY_NOIROLABS', 'QUAY_NOIRO_TOKEN', 'DOCKER_NOIRO', 'GITHUBACTIONS_TAGGER', 'GHA_TRIGGER_TAG']:
            self.assertNotIn(name, self.path.read_text())
        self.assertEqual(self.job['env']['TRAVIS_REPO_SLUG'], '${{ github.repository }}')

    def test_sha_gate_precedes_selected_source_helper_and_credentials(self):
        self.assertEqual(self.steps[0]['name'], 'Require reviewed source and helper before selected code runs')
        gate = self.steps[0]
        self.assertNotIn('secrets.', str(gate))
        for values, success in [({}, True), ({'REVIEWED_SOURCE_SHA': ''}, False),
              ({'REVIEWED_CICD_SHA': ''}, False), ({'TRAVIS_COMMIT': 'c'*40}, False),
              ({'CICD_COMMIT': 'c'*40}, False)]:
            result, _ = self.run_script(gate['run'], values)
            self.assertEqual(result.returncode == 0, success, result.stderr)
        checkout = self.step('Check out validated source')
        self.assertEqual(checkout['with']['ref'], '${{ needs.resolve.outputs.source-sha }}')

    def test_normalized_image_targets_serialize_streams_without_cancellation(self):
        lock = self.job['concurrency']
        self.assertFalse(lock['cancel-in-progress'])
        self.assertIn('needs.resolve.outputs.release-tag', lock['group'])
        self.assertIn('needs.resolve.outputs.operation', lock['group'])
        self.assertNotIn('stream', lock['group'])
        self.assertNotIn('source-sha', lock['group'])
        self.assertEqual(self.job['env']['EXPECTED_OPFLEX_BASE_DIGEST'], '${{ needs.resolve.outputs.base-digest }}')

    def test_actual_login_only_uses_private_quay(self):
        result, files = self.run_script(self.step('Authenticate to private rehearsal registry')['run'],
            {'QUAY_USERNAME': 'fake-user', 'QUAY_TOKEN': 'fake-token'},
            {'docker': 'printf "%s\\n" "$@" > "$FIXTURE_ARGS"\ncat >/dev/null\n'})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(files['args'].splitlines(), ['login', 'quay.io', '--username', 'fake-user', '--password-stdin'])

    def test_actual_builder_receives_captured_digest_without_resolving_a_mutable_base(self):
        step = next(step for step in self.steps if step.get('id') == 'build')
        helper = 'cicd/travis/build-push-' + self.repo + '-images.sh'
        digest = 'sha256:' + 'f'*64
        result, files = self.run_script(step['run'],
            {'EXPECTED_OPFLEX_BASE_DIGEST': digest}, {helper:
                'printf "%s\\n" "$EXPECTED_OPFLEX_BASE_DIGEST" "$SKIP_CICD_STATUS" "${GHA_TRIGGER_TAG-unset}" > "$FIXTURE_ARGS"\n'})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(files['args'].splitlines(), [digest, 'true', 'unset'])
        self.assertNotIn('docker pull', step['run'])
        self.assertEqual(set(step['env']), {'CI_QUAY_USERNAME', 'CI_QUAY_TOKEN'})

    def test_actual_status_command_uses_only_fixed_test_destination(self):
        step = self.step('Publish prepared image status')
        self.assertIn("vars.REHEARSAL_PUBLISH_STATUS == 'true'", step['if'])
        self.assertEqual(step['env'], {'TRAVIS_TAGGER': '${{ secrets.TEST_CICD_STATUS_TOKEN }}'})
        result, files = self.run_script(step['run'], binaries={'python3': 'printf "%s\\n" "$@" > "$FIXTURE_ARGS"\n'})
        self.assertEqual(result.returncode, 0, result.stderr)
        args = files['args'].splitlines()
        self.assertEqual(args[args.index('--repository')+1], 'https://github.com/noironetworks/test-cicd-status.git')
        self.assertEqual(args[args.index('--branch')+1], 'anmrasto/gha-rehearsal-1.2.3.4')
        self.assertNotIn('https://github.com/noironetworks/cicd-status.git', args)

    def test_actual_evidence_keeps_manual_source_identity_without_fake_tag(self):
        result, files = self.run_script(self.step('Prepare immutable image evidence')['run'],
            binaries={'python3': 'printf "%s\\n" "$@" > "$FIXTURE_ARGS"\n'}, metadata=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        args = files['args'].splitlines()
        self.assertEqual(args[args.index('--source-ref')+1], 'reviewed/source')
        self.assertNotIn('--trigger-tag', args)
        self.assertIn('--expected-images', args)

    def test_every_actual_shell_block_parses(self):
        for job in self.workflow['jobs'].values():
            for step in job['steps']:
                if 'run' in step:
                    self.assertNotRegex(step['run'], r'\$\{\{\s*inputs\.')
                    result = subprocess.run(['bash', '-n'], input=step['run'], text=True, capture_output=True)
                    self.assertEqual(result.returncode, 0, f"{step['name']}: {result.stderr}")
