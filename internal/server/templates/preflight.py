"""Host-side checks before running the standalone generated Compose project."""
import json
import hashlib
import os
from pathlib import Path
import tempfile
import subprocess
import sys

directory = Path(__file__).resolve().parent
manifest = json.loads((directory / 'stack.json').read_text())
spec = manifest['spec']
artifact = spec.get('artifact_path')
if spec['artifacts'] == 'nas':
    mount = spec['nas_mount']
    if not os.path.ismount(os.path.realpath(mount)) or os.path.realpath(mount) == '/':
        raise SystemExit('NAS is not mounted; refusing to write into the local mountpoint')
    resolved = os.path.realpath(mount)
    resolved_artifact = os.path.realpath(artifact)
    if os.path.commonpath([resolved, resolved_artifact]) != resolved:
        raise SystemExit('Resolved artifact directory escapes the NAS mount through a symlink')
    if sys.platform == 'linux':
        data = json.loads(subprocess.check_output(['findmnt', '--json', '--output', 'TARGET,FSTYPE,SOURCE', '--mountpoint', resolved], timeout=5))['filesystems'][0]
        kind, source = data['fstype'], data['source']
    elif sys.platform == 'darwin':
        lines = subprocess.check_output(['/sbin/mount'], text=True, timeout=5).splitlines()
        line = next(line for line in lines if ' on ' + resolved + ' (' in line)
        source, options = line.split(' on ' + resolved + ' (', 1)
        kind = options.split(',', 1)[0].rstrip(')')
    else:
        raise SystemExit('NAS mount verification requires Linux or macOS')
    identity = hashlib.sha256((kind + '\n' + source).encode()).hexdigest()
    if identity != manifest.get('nas_mount_identity'):
        raise SystemExit('NAS mount source or filesystem changed; verify the mount before starting')
if artifact:
    if not Path(artifact).is_dir():
        raise SystemExit('Configured artifact directory does not exist')
    with tempfile.TemporaryFile(dir=artifact) as stream:
        stream.write(b'lazymlflow preflight')
        stream.flush()
        os.fsync(stream.fileno())
print('Host storage checks passed. Container UID/GID 1000 must also have write access.')
