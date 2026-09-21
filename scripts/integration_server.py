#!/usr/bin/env python3
"""Disposable Docker integration tests for generated stacks, never existing servers.

Only projects generated in this invocation are removed (including their test
volumes). Shared base images, other projects and user tracking targets are untouched.
"""
import argparse
import base64
import contextlib
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import ssl
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def run(args, **kwargs):
    result = subprocess.run(args, text=True, capture_output=True, **kwargs)
    if result.returncode:
        raise RuntimeError('Command failed: ' + ' '.join(map(str, args[:4])) + '\n' + result.stderr[-12000:])
    return result.stdout


def request(base, path, *, data=None, method=None, credentials=None, context=None, binary=False):
    headers = {}
    if credentials:
        headers['Authorization'] = 'Basic ' + base64.b64encode((':'.join(credentials)).encode()).decode()
    if data is not None:
        if not isinstance(data, bytes):
            data = json.dumps(data).encode()
            headers['Content-Type'] = 'application/json'
        else:
            headers['Content-Type'] = 'application/octet-stream'
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    with urllib.request.urlopen(req, context=context, timeout=30) as response:
        body = response.read()
        return body if binary else (json.loads(body) if body else {})


@contextlib.contextmanager
def external_s3_fixture(binary, root):
    directory = root / 'external-source'
    run([binary, 'server', 'init', 'external-source', '--dir', str(directory), '--backend', 'postgres', '--artifacts', 'rustfs', '--json'])
    stack = json.loads((directory / 'stack.json').read_text())
    compose = ['docker', 'compose', '--project-name', stack['project'], '--project-directory', str(directory), '--file', str(directory / 'compose.yaml')]
    env = dict(os.environ, PWD=str(directory))
    try:
        run(compose + ['up', '--detach', 'storage'], env=env, timeout=300)
        run(compose + ['run', '--build', '--rm', '--no-deps', 'storage-init'], env=env, timeout=600)
        container = run(compose + ['ps', '--quiet', 'storage'], env=env).strip()
        yield {'container': container, 'access': (directory / 'secrets/s3_access_key').read_text(),
               'secret': (directory / 'secrets/s3_secret_key').read_text()}
    finally:
        subprocess.run(compose + ['down', '--volumes', '--timeout', '10'], env=env, capture_output=True, timeout=120)
        subprocess.run(['docker', 'image', 'rm', stack['project'] + '-mlflow:3.16.1'], capture_output=True, timeout=60)


def smoke(binary, root, profile, external=None):
    if profile == 'external-s3' and external is None:
        print('external-s3: preparing an isolated existing S3 service and bucket', flush=True)
        with external_s3_fixture(binary, root) as fixture:
            return smoke(binary, root, profile, external=fixture)
    directory = root / profile
    port = free_port()
    args = [binary, 'server', 'init', 'test-' + profile, '--dir', str(directory), '--port', str(port)]
    if profile in ('postgres', 'rustfs', 'seaweedfs', 'pg-auth', 'external-s3'):
        args += ['--backend', 'postgres']
    if profile in ('rustfs', 'seaweedfs'):
        args += ['--artifacts', profile]
    if profile in ('auth', 'pg-auth'):
        args += ['--tls', 'internal', '--auth', 'native', '--hostname', 'localhost']
    if profile == 'provided':
        cert, key = root / 'provided.crt', root / 'provided.key'
        run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
             '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost',
             '-keyout', str(key), '-out', str(cert)])
        args += ['--tls', 'provided', '--cert-file', str(cert), '--key-file', str(key), '--hostname', 'localhost']
    init_env = dict(os.environ)
    if external:
        args += ['--artifacts', 's3', '--s3-endpoint', 'http://external-storage:9000', '--s3-bucket', 'mlflow',
                 '--s3-access-key-env', 'LAZYMLFLOW_SMOKE_ACCESS', '--s3-secret-key-env', 'LAZYMLFLOW_SMOKE_SECRET']
        init_env.update(LAZYMLFLOW_SMOKE_ACCESS=external['access'], LAZYMLFLOW_SMOKE_SECRET=external['secret'])
    print(profile + ': generating', flush=True)
    run(args + ['--json'], env=init_env)
    stack = json.loads((directory / 'stack.json').read_text())
    compose = ['docker', 'compose', '--project-name', stack['project'], '--project-directory', str(directory),
               '--file', str(directory / 'compose.yaml')]
    env = dict(os.environ, PWD=str(directory))
    def connect_external():
        run(['docker', 'network', 'create', '--label', 'com.docker.compose.project=' + stack['project'],
             '--label', 'com.docker.compose.network=default', '--label', 'io.lazymlflow.owner=' + stack['project'],
             '--label', 'io.lazymlflow.stack-directory=' + str(directory), stack['project'] + '_default'])
        run(['docker', 'network', 'connect', '--alias', 'external-storage', stack['project'] + '_default', external['container']])
    try:
        if external:
            # The S3 fixture is external to the generated project, with no host
            # ports. Join it to the test's private network under a fixed alias.
            connect_external()
        print(profile + ': building and starting', flush=True)
        run([binary, 'server', 'up', '--dir', str(directory)], timeout=1200)
        context = None
        if profile in ('auth', 'pg-auth', 'provided'):
            ca = root / 'provided.crt' if profile == 'provided' else directory / 'ca.crt'
            context = ssl.create_default_context(cafile=str(ca))
            original_ca = ca.read_bytes()
            try:
                request(stack['tracking_uri'], '/health', context=ssl.create_default_context())
                raise AssertionError('Untrusted certificate was accepted')
            except urllib.error.URLError as exc:
                assert isinstance(exc.reason, ssl.SSLCertVerificationError), exc
        credentials = None
        old_password = None
        if profile in ('auth', 'pg-auth'):
            old_password = (directory / 'secrets/admin_password').read_text()
            credentials = ('admin', old_password)
            try:
                request(stack['tracking_uri'], '/api/2.0/mlflow/experiments/search', data={'max_results': 1}, context=context)
                raise AssertionError('Anonymous tracking access succeeded')
            except urllib.error.HTTPError as exc:
                assert exc.code == 401, exc.code

        def api(path, **kwargs):
            return request(stack['tracking_uri'], path, credentials=credentials, context=context, **kwargs)

        runtime_version = api('/version', binary=True).decode().strip()
        assert runtime_version == '3.16.1', runtime_version
        exp = api('/api/2.0/mlflow/experiments/create', data={'name': 'disposable-smoke'})['experiment_id']
        api('/api/2.0/mlflow/registered-models/create', data={'name': 'disposable-registry-smoke'})
        result = api('/api/2.0/mlflow/runs/create', data={'experiment_id': exp, 'start_time': int(time.time() * 1000)})['run']
        run_id = result['info']['run_id']
        api('/api/2.0/mlflow/runs/log-metric', data={'run_id': run_id, 'key': 'smoke', 'value': 2.5, 'timestamp': int(time.time() * 1000), 'step': 1})
        uri = result['info']['artifact_uri']
        assert uri.startswith('mlflow-artifacts:'), uri
        artifact = urllib.parse.urlsplit(uri).path.strip('/') + '/unicode space-測試.txt'
        route = '/api/2.0/mlflow-artifacts/artifacts/' + urllib.parse.quote(artifact, safe='/')
        payload = b'disposable artifact contents\n'
        api(route, method='PUT', data=payload, binary=True)
        downloaded = api(route, binary=True)
        assert hashlib.sha256(downloaded).digest() == hashlib.sha256(payload).digest()
        listing = api('/api/2.0/mlflow/artifacts/list?' + urllib.parse.urlencode({'run_id': run_id}))
        assert any(row['path'] == 'unicode space-測試.txt' for row in listing['files']), listing
        if profile in ('rustfs', 'seaweedfs'):
            # Test S3 multipart without publishing the object-store endpoint or
            # copying credentials into the training client.
            multipart = """
import sys,hashlib,uuid
sys.path.insert(0,'/opt/lazymlflow')
from runtime import configure
configure()
import boto3,os
from botocore.config import Config
client=boto3.client('s3',endpoint_url=os.environ['MLFLOW_S3_ENDPOINT_URL'],region_name='us-east-1',config=Config(s3={'addressing_style':'path'}))
bucket=os.environ['S3_BUCKET']; key='smoke-multipart/'+uuid.uuid4().hex
parts=[b'a'*(6*1024*1024),b'end']
upload=client.create_multipart_upload(Bucket=bucket,Key=key)['UploadId']
try:
 result=[]
 for number,data in enumerate(parts,1):
  etag=client.upload_part(Bucket=bucket,Key=key,UploadId=upload,PartNumber=number,Body=data)['ETag']
  result.append({'ETag':etag,'PartNumber':number})
 client.complete_multipart_upload(Bucket=bucket,Key=key,UploadId=upload,MultipartUpload={'Parts':result})
 body=client.get_object(Bucket=bucket,Key=key)['Body'].read()
 assert hashlib.sha256(body).digest()==hashlib.sha256(b''.join(parts)).digest()
 client.delete_object(Bucket=bucket,Key=key)
except:
 client.abort_multipart_upload(Bucket=bucket,Key=key,UploadId=upload)
 raise
print('multipart roundtrip passed')
"""
            run(compose + ['exec', '--no-TTY', 'mlflow', 'python', '-c', multipart], env=env, timeout=90)
        if profile in ('auth', 'pg-auth'):
            member_password = secrets.token_hex(24)
            api('/api/2.0/mlflow/users/create', data={'username': 'unprivileged-smoke', 'password': member_password})
            try:
                request(stack['tracking_uri'], route, credentials=('unprivileged-smoke', member_password), context=context, binary=True)
                raise AssertionError('Unprivileged member downloaded an artifact')
            except urllib.error.HTTPError as exc:
                assert exc.code == 403, exc.code
            rotated = secrets.token_hex(24)
            api('/api/2.0/mlflow/users/update-password', method='PATCH', data={'username': 'admin', 'password': rotated, 'current_password': old_password})
            credentials = ('admin', rotated)

        print(profile + ': verifying persistence and idempotent startup', flush=True)
        if external:
            run(['docker', 'network', 'disconnect', stack['project'] + '_default', external['container']])
        run([binary, 'server', 'down', '--dir', str(directory)], timeout=90)
        if external:
            connect_external()
        run([binary, 'server', 'up', '--dir', str(directory)], timeout=1200)
        persisted = api('/api/2.0/mlflow/runs/get?' + urllib.parse.urlencode({'run_id': run_id}))
        assert persisted['run']['data']['metrics'][0]['value'] == 2.5
        assert api(route, binary=True) == payload
        registered = api('/api/2.0/mlflow/registered-models/get?name=disposable-registry-smoke')
        assert registered['registered_model']['name'] == 'disposable-registry-smoke'
        if profile in ('auth', 'pg-auth'):
            assert (directory / 'ca.crt').read_bytes() == original_ca, 'CA changed across restart'
            try:
                request(stack['tracking_uri'], '/api/2.0/mlflow/experiments/search', data={'max_results': 1},
                        credentials=('admin', old_password), context=context)
                raise AssertionError('Restart restored the old bootstrap password')
            except urllib.error.HTTPError as exc:
                assert exc.code == 401, exc.code
        status = json.loads(run([binary, 'server', 'status', '--dir', str(directory), '--json']))
        assert any(s['service'] == 'mlflow' and s['health'] == 'healthy' for s in status['services'])
        if profile in ('auth', 'pg-auth'):
            # Deleting the initialized administrator is an explicit repair case,
            # not permission to resurrect the retained bootstrap credential.
            api('/api/2.0/mlflow/users/update-admin', method='PATCH', data={'username': 'unprivileged-smoke', 'is_admin': True})
            request(stack['tracking_uri'], '/api/2.0/mlflow/users/delete', method='DELETE', data={'username': 'admin'},
                    credentials=('unprivileged-smoke', member_password), context=context)
            run([binary, 'server', 'down', '--dir', str(directory)], timeout=90)
            repair = subprocess.run([binary, 'server', 'up', '--dir', str(directory)], text=True, capture_output=True, timeout=300)
            assert repair.returncode != 0 and 'repair native auth explicitly' in repair.stderr, repair.stderr[-2000:]
            check_absent = "import sys;sys.path.insert(0,'/opt/lazymlflow');from runtime import configure;configure();from mlflow.server.auth import store,auth_config;store.init_db(auth_config.database_uri);assert not store.has_user('admin');assert store.get_user('unprivileged-smoke').is_admin;print('deleted administrator was not recreated')"
            run(compose + ['run', '--rm', '--no-deps', 'mlflow', 'python', '-c', check_absent], env=env, timeout=90)
        print(profile + ': passed', flush=True)
        return {'profile': profile, 'status': 'passed', 'mlflow_version': runtime_version, 'artifact_sha256': hashlib.sha256(payload).hexdigest()}
    except Exception:
        logs = subprocess.run([binary, 'server', 'logs', '--dir', str(directory), '--tail', '40'], text=True, capture_output=True)
        print(logs.stdout[-16000:], flush=True)
        raise
    finally:
        # Explicitly disposable test resources only. Product `server down`
        # deliberately offers no volume-deleting flag.
        if external:
            subprocess.run(['docker', 'network', 'disconnect', stack['project'] + '_default', external['container']], capture_output=True, timeout=30)
        subprocess.run(compose + ['down', '--volumes', '--timeout', '10'], env=env, capture_output=True, timeout=120)
        subprocess.run(['docker', 'image', 'rm', stack['project'] + '-mlflow:3.16.1'], capture_output=True, timeout=60)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', default='./bin/lazymlflow')
    parser.add_argument('--profiles', default='sqlite,auth,pg-auth,postgres,rustfs,seaweedfs,external-s3,provided')
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    profiles = args.profiles.split(',')
    if set(profiles) - {'sqlite', 'auth', 'pg-auth', 'postgres', 'rustfs', 'seaweedfs', 'external-s3', 'provided'}:
        parser.error('unknown profile')
    with tempfile.TemporaryDirectory(prefix='lazymlflow-server-smoke-') as tmp:
        results = [smoke(binary, Path(tmp), profile) for profile in profiles]
    print(json.dumps({'results': results, 'nas': 'Not exercised without an explicitly supplied NAS mount; missing-mount behavior has unit coverage.'}, indent=2))


if __name__ == '__main__':
    main()
