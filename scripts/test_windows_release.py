import io
import struct
import tarfile
import tempfile
import unittest
import zipfile
from pathlib import Path
import release

class WindowsReleaseTests(unittest.TestCase):
    def fixture(self, root):
        version = '.'.join(map(str, release.WINDOWS_SINCE))
        rows = []
        for system, arch in release.release_targets(version):
            name = release.archive_name('example', version, system, arch)
            exe = 'example.exe' if system == 'windows' else 'example'
            if system == 'windows':
                binary = bytearray(128)
                binary[:2] = b'MZ'
                struct.pack_into('<I', binary, 60, 64)
                binary[64:68] = b'PE\0\0'
                struct.pack_into('<H', binary, 68, {'amd64':0x8664,'arm64':0xaa64}[arch])
                struct.pack_into('<H', binary, 88, 0x20b)
            elif system == 'linux':
                binary = bytearray(20); binary[:6] = b'\x7fELF\x02\x01'
                struct.pack_into('<H', binary, 18, {'amd64':62,'arm64':183}[arch])
            else:
                binary = bytearray(b'\xcf\xfa\xed\xfe' + b'\0'*4)
                struct.pack_into('<I', binary, 4, {'amd64':0x01000007,'arm64':0x0100000c}[arch])
            files = {exe:bytes(binary),'LICENSE':b'MIT','completions/example.bash':b'bash','completions/example.zsh':b'zsh','completions/example.ps1':b'powershell'}
            if system == 'windows':
                with zipfile.ZipFile(root/name,'w') as z:
                    for path,data in files.items(): z.writestr(path,data)
            else:
                with tarfile.open(root/name,'w:gz') as t:
                    for path,data in files.items():
                        info=tarfile.TarInfo(path); info.size=len(data); info.mode=0o755 if path==exe else 0o644; t.addfile(info,io.BytesIO(data))
            rows.append(f'{release.digest(root/name)}  {name}')
        source=f'example_{version}_source.tar.gz'
        with tarfile.open(root/source,'w:gz') as t:
            for path,data in {'go.mod':b'module fixture.invalid/example','go.sum':b'','LICENSE':b'MIT'}.items():
                info=tarfile.TarInfo(path);info.size=len(data);t.addfile(info,io.BytesIO(data))
        rows.append(f'{release.digest(root/source)}  {source}')
        (root/'checksums.txt').write_text('\n'.join(rows)+'\n')
        return version

    def test_complete_windows_and_unix_inventory(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);version=self.fixture(root)
            self.assertEqual(len(release.verify_dist(root,'example','example',version)),8)
            windows=root/release.archive_name('example',version,'windows','amd64')
            windows.write_bytes(b'corrupt')
            with self.assertRaisesRegex(ValueError,'checksum mismatch'):release.verify_dist(root,'example','example',version)

    def test_wrong_pe_architecture_and_truncated_header(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);version=self.fixture(root)
            with zipfile.ZipFile(root/release.archive_name('example',version,'windows','amd64')) as z: data=z.read('example.exe')
            release.verify_binary(data,'windows','amd64')
            for bad,arch in [(data,'arm64'),(data[:66],'amd64'),(b'MZ'+b'\xff'*62,'amd64')]:
                with self.assertRaisesRegex(ValueError,'does not match'):release.verify_binary(bad,'windows',arch)

    def test_zip_extra_path_rejected_even_with_matching_checksum(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);version=self.fixture(root)
            name=release.archive_name('example',version,'windows','amd64')
            with zipfile.ZipFile(root/name,'a') as z:z.writestr('../outside',b'unsafe')
            lines=(root/'checksums.txt').read_text().splitlines()
            (root/'checksums.txt').write_text('\n'.join(f'{release.digest(root/name)}  {name}' if line.split()[1]==name else line for line in lines)+'\n')
            with self.assertRaisesRegex(ValueError,'unexpected archive entries'):release.verify_dist(root,'example','example',version)

    def test_legacy_contract_does_not_gain_windows_assets(self):
        self.assertEqual(release.release_targets('0.1.0'),release.TARGETS)
        self.assertEqual(len(release.release_targets('snapshot')),6)

if __name__=='__main__':unittest.main()
