"""Pinned MLflow 3.16.1 stack entrypoints; no credential values are logged."""
import configparser
import json
import os
from pathlib import Path
from urllib.parse import quote


def secret(name):
    key = {'postgres_password': 'POSTGRES_PASSWORD', 'csrf_secret': 'MLFLOW_FLASK_SERVER_SECRET_KEY',
           'admin_password': 'LAZYMLFLOW_BOOTSTRAP_PASSWORD', 's3_access_key': 'AWS_ACCESS_KEY_ID',
           's3_secret_key': 'AWS_SECRET_ACCESS_KEY'}[name]
    value = os.environ.get(key)
    if not value:
        raise RuntimeError('Required private configuration is missing: ' + name)
    return value


def database_uri(name):
    if os.environ['BACKEND_KIND'] == 'sqlite':
        return 'sqlite:////data/' + ('auth.db' if name == 'mlflow_auth' else 'mlflow.db')
    password = quote(secret('postgres_password'), safe='')
    return f'postgresql+psycopg2://mlflow:{password}@postgres:5432/{name}'


def configure():
    # Compose never passes a bootstrap password to the regular server. Remove
    # inherited knobs too, so restoring an old account cannot trigger rotation.
    os.environ.pop('MLFLOW_AUTH_ADMIN_PASSWORD', None)
    os.environ['MLFLOW_BACKEND_STORE_URI'] = database_uri('mlflow')
    if os.environ['ARTIFACT_KIND'] in ('s3', 'rustfs', 'seaweedfs'):
        os.environ['AWS_ACCESS_KEY_ID'] = secret('s3_access_key')
        os.environ['AWS_SECRET_ACCESS_KEY'] = secret('s3_secret_key')
    if os.environ['AUTH_MODE'] == 'native':
        os.environ['MLFLOW_FLASK_SERVER_SECRET_KEY'] = secret('csrf_secret')
        config = configparser.ConfigParser()
        config['mlflow'] = {
            'default_permission': 'NO_PERMISSIONS',
            'database_uri': database_uri('mlflow_auth').replace('%', '%%'),
            'admin_username': os.environ['MLFLOW_AUTH_ADMIN_USERNAME'].replace('%', '%%'),
            'authorization_function': 'mlflow.server.auth:authenticate_request_basic_auth',
        }
        path = '/tmp/lazymlflow-auth.ini'
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, 'w') as stream:
            config.write(stream)
        os.environ['MLFLOW_AUTH_CONFIG_PATH'] = path


def ensure_auth_database():
    if os.environ['BACKEND_KIND'] != 'postgres':
        return
    import psycopg2
    from psycopg2 import sql
    connection = psycopg2.connect(host='postgres', dbname='mlflow', user='mlflow',
                                 password=secret('postgres_password'))
    connection.autocommit = True
    try:
        with connection.cursor() as cursor:
            cursor.execute('SELECT 1 FROM pg_database WHERE datname=%s', ('mlflow_auth',))
            if cursor.fetchone() is None:
                try:
                    cursor.execute(sql.SQL('CREATE DATABASE {}').format(sql.Identifier('mlflow_auth')))
                except psycopg2.errors.DuplicateDatabase:
                    pass
    finally:
        connection.close()


def auth_init():
    ensure_auth_database()
    configure()
    from mlflow.exceptions import MlflowException
    from mlflow.server.auth import (store, auth_config, _validate_bootstrap_admin_username,
                                    _validate_bootstrap_admin_password)
    store.init_db(auth_config.database_uri)
    username = auth_config.admin_username
    _validate_bootstrap_admin_username(username)
    receipt_path = Path('/data/auth-bootstrap.json')
    identity = {'schema_version': 1, 'admin_username': username,
                'backend': os.environ['BACKEND_KIND']}
    if receipt_path.exists():
        if json.loads(receipt_path.read_text()) != identity:
            raise RuntimeError('Auth bootstrap identity changed; restore the original configuration or repair auth explicitly')
        if not store.has_user(username, use_primary=True) or not store.get_user(username).is_admin:
            raise RuntimeError('Previously initialized administrator is missing or no longer an admin; repair native auth explicitly (no account recreated)')
        print('Native auth bootstrap receipt verified; credentials and grants preserved.')
        return
    if store.has_user(username, use_primary=True):
        if not store.get_user(username).is_admin:
            raise RuntimeError('Configured bootstrap username belongs to a non-admin; refusing to change it')
        print('Native auth administrator already exists; credentials and grants preserved.')
        receipt_path.write_text(json.dumps(identity) + '\n')
        receipt_path.chmod(0o600)
        return
    password = secret('admin_password')
    _validate_bootstrap_admin_password(username, password)
    try:
        store.create_user(username, password, is_admin=True)
    except MlflowException as exc:
        if exc.error_code != 'RESOURCE_ALREADY_EXISTS':
            raise
        if not store.get_user(username).is_admin:
            raise RuntimeError('Concurrent bootstrap found a non-admin; refusing to change it') from None
    receipt_path.write_text(json.dumps(identity) + '\n')
    receipt_path.chmod(0o600)
    print('Native auth administrator initialized. Manage users and roles in /admin.')


def storage_init():
    import time
    import boto3
    from botocore.config import Config
    from botocore.exceptions import ClientError, EndpointConnectionError, ConnectionClosedError
    configure()
    client = boto3.client('s3', endpoint_url=os.environ.get('MLFLOW_S3_ENDPOINT_URL') or None,
                          region_name=os.environ['AWS_DEFAULT_REGION'],
                          config=Config(s3={'addressing_style': 'path'},
                                        retries={'max_attempts': 2}, connect_timeout=3, read_timeout=5))
    bucket = os.environ['S3_BUCKET']
    managed = os.environ['ARTIFACT_KIND'] != 's3'
    for attempt in range(30):
        try:
            client.head_bucket(Bucket=bucket)
            print('S3 bucket is accessible with the configured credentials.')
            return
        except ClientError as exc:
            code = str(exc.response.get('Error', {}).get('Code', ''))
            if code in ('401', '403', 'AccessDenied', 'InvalidAccessKeyId', 'SignatureDoesNotMatch'):
                raise RuntimeError('S3 authentication/permission check failed; no bucket or policy was changed') from None
            if code in ('404', 'NoSuchBucket'):
                if not managed:
                    raise RuntimeError('External S3 bucket does not exist; create it outside lazymlflow') from None
                try:
                    args = {'Bucket': bucket}
                    if os.environ['AWS_DEFAULT_REGION'] != 'us-east-1':
                        args['CreateBucketConfiguration'] = {'LocationConstraint': os.environ['AWS_DEFAULT_REGION']}
                    client.create_bucket(**args)
                except ClientError as create_exc:
                    if str(create_exc.response.get('Error', {}).get('Code', '')) not in ('BucketAlreadyOwnedByYou', 'BucketAlreadyExists'):
                        raise RuntimeError('Managed S3 bucket creation failed') from None
            elif code not in ('500', '503', 'InternalError', 'ServiceUnavailable'):
                raise RuntimeError('S3 bucket check failed: ' + code) from None
        except (EndpointConnectionError, ConnectionClosedError):
            pass
        if attempt < 29:
            time.sleep(2)
    raise RuntimeError('S3 bucket readiness timed out')


def server():
    configure()
    if os.environ['ARTIFACT_KIND'] in ('local', 'nas'):
        import tempfile
        directory = os.environ['MLFLOW_ARTIFACTS_DESTINATION']
        with tempfile.TemporaryFile(dir=directory) as stream:
            stream.write(b'lazymlflow filesystem check')
            stream.flush()
            os.fsync(stream.fileno())
    args = ['mlflow', 'server', '--host', '0.0.0.0', '--port', '5000',
            '--backend-store-uri', os.environ['MLFLOW_BACKEND_STORE_URI'],
            '--serve-artifacts', '--artifacts-destination', os.environ['MLFLOW_ARTIFACTS_DESTINATION'],
            '--allowed-hosts', os.environ['MLFLOW_SERVER_ALLOWED_HOSTS']]
    if os.environ['AUTH_MODE'] == 'native':
        args += ['--app-name', 'basic-auth']
    os.execvp(args[0], args)


if __name__ == '__main__':
    import sys
    actions = {'server': server, 'auth-init': auth_init, 'storage-init': storage_init}
    if len(sys.argv) != 2 or sys.argv[1] not in actions:
        raise SystemExit('Expected server, auth-init or storage-init')
    actions[sys.argv[1]]()
