#!/usr/bin/env python3
"""Build platform wheels that ship the interlinked Go binary.

Each wheel puts the static binary in `<name>.data/scripts/`, so pip/pipx/uv
install it straight onto PATH (the same layout ruff and uv use). No Python
runtime is involved at run time. Standard library only.

    python packaging/pypi/build_wheels.py --version 0.1.0            # all platforms
    python packaging/pypi/build_wheels.py --version 0.1.0 --only windows-amd64
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import os
import pathlib
import subprocess
import sys
import zipfile

DIST_NAME = "ainterlinked"  # PyPI name ("interlinked" is taken by another project)
PKG = "ainterlinked"  # import package (python -m ainterlinked)
GO_MAIN = "./cmd/interlinked"
LDFLAG_VERSION = "github.com/romanpert/interlinked/internal/cli.Version"
SUMMARY = "Real-time bus for AI coding agents across harnesses: Claude Code, Codex, Copilot, Cursor, Gemini, OpenCode and more"

# goos, goarch -> wheel platform tags (Go binaries are static: one wheel covers glibc and musl).
TARGETS = {
    "linux-amd64": ("linux", "amd64", ["manylinux_2_17_x86_64", "manylinux2014_x86_64", "musllinux_1_1_x86_64"]),
    "linux-arm64": ("linux", "arm64", ["manylinux_2_17_aarch64", "manylinux2014_aarch64", "musllinux_1_1_aarch64"]),
    "darwin-amd64": ("darwin", "amd64", ["macosx_10_12_x86_64"]),
    "darwin-arm64": ("darwin", "arm64", ["macosx_11_0_arm64"]),
    "windows-amd64": ("windows", "amd64", ["win_amd64"]),
    "windows-arm64": ("windows", "arm64", ["win_arm64"]),
}

ROOT = pathlib.Path(__file__).resolve().parents[2]


def build_binary(goos: str, goarch: str, version: str, out: pathlib.Path) -> pathlib.Path:
    exe = out / f"interlinked-{goos}-{goarch}{'.exe' if goos == 'windows' else ''}"
    env = {**os.environ, "GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0"}
    cmd = ["go", "build", "-trimpath", "-ldflags", f"-s -w -X {LDFLAG_VERSION}={version}", "-o", str(exe), GO_MAIN]
    subprocess.run(cmd, cwd=ROOT, env=env, check=True)
    return exe


def record_line(path: str, data: bytes) -> str:
    digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
    return f"{path},sha256={digest},{len(data)}"


def metadata(version: str) -> str:
    readme = (ROOT / "README.md").read_text(encoding="utf-8")
    lines = [
        "Metadata-Version: 2.1",
        f"Name: {DIST_NAME}",
        f"Version: {version}",
        f"Summary: {SUMMARY}",
        "Home-page: https://github.com/romanpert/interlinked",
        "Project-URL: Source, https://github.com/romanpert/interlinked",
        "Project-URL: Issues, https://github.com/romanpert/interlinked/issues",
        "License: Apache-2.0",
        "Keywords: ai,agents,mcp,claude-code,codex,copilot,cursor,gemini,opencode,multi-agent",
        "Classifier: Development Status :: 4 - Beta",
        "Classifier: Environment :: Console",
        "Classifier: Intended Audience :: Developers",
        "Classifier: License :: OSI Approved :: Apache Software License",
        "Classifier: Operating System :: OS Independent",
        "Classifier: Programming Language :: Go",
        "Classifier: Topic :: Software Development",
        "Requires-Python: >=3.8",
        "Description-Content-Type: text/markdown",
        "",
        readme,
    ]
    return "\n".join(lines)


INIT = '''"""interlinked: real-time bus for AI coding agents. The work is done by the
bundled `interlinked` executable; this module only locates and runs it."""
import os
import sys
import sysconfig

__version__ = "{version}"


def binary_path() -> str:
    """Absolute path of the bundled interlinked executable."""
    name = "interlinked.exe" if os.name == "nt" else "interlinked"
    candidates = [sysconfig.get_path("scripts")]
    user = sysconfig.get_path("scripts", f"{{os.name}}_user") if os.name else None
    if user:
        candidates.append(user)
    for d in candidates:
        p = os.path.join(d, name)
        if os.path.isfile(p):
            return p
    raise FileNotFoundError(f"{{name}} not found next to this Python ({{candidates}})")
'''

MAIN = '''import os
import subprocess
import sys

from . import binary_path


def main() -> int:
    exe = binary_path()
    if os.name == "nt":
        return subprocess.call([exe, *sys.argv[1:]])
    os.execv(exe, [exe, *sys.argv[1:]])
    return 0


if __name__ == "__main__":
    sys.exit(main())
'''


def build_wheel(key: str, version: str, binary: pathlib.Path, dist: pathlib.Path) -> pathlib.Path:
    goos, _, tags = TARGETS[key]
    tag = ".".join(tags)
    wheel_name = f"{DIST_NAME.replace('-', '_')}-{version}-py3-none-{tag}.whl"
    distinfo = f"{DIST_NAME.replace('-', '_')}-{version}.dist-info"
    data = f"{DIST_NAME.replace('-', '_')}-{version}.data"
    exe_name = "interlinked.exe" if goos == "windows" else "interlinked"
    files: list[tuple[str, bytes, int]] = [
        (f"{PKG}/__init__.py", INIT.format(version=version).encode(), 0o644),
        (f"{PKG}/__main__.py", MAIN.encode(), 0o644),
        (f"{data}/scripts/{exe_name}", binary.read_bytes(), 0o755),
        (f"{distinfo}/METADATA", metadata(version).encode(), 0o644),
        (f"{distinfo}/licenses/LICENSE", (ROOT / "LICENSE").read_bytes(), 0o644),
        (f"{distinfo}/WHEEL", ("Wheel-Version: 1.0\nGenerator: interlinked build_wheels.py\nRoot-Is-Purelib: false\n"
                                + "".join(f"Tag: py3-none-{t}\n" for t in tags)).encode(), 0o644),
    ]
    record = [record_line(p, d) for p, d, _ in files] + [f"{distinfo}/RECORD,,"]
    files.append((f"{distinfo}/RECORD", ("\n".join(record) + "\n").encode(), 0o644))
    dist.mkdir(parents=True, exist_ok=True)
    out = dist / wheel_name
    with zipfile.ZipFile(out, "w", compression=zipfile.ZIP_DEFLATED) as z:
        for path, content, mode in files:
            info = zipfile.ZipInfo(path, date_time=(2026, 1, 1, 0, 0, 0))
            info.external_attr = (mode | 0o100000) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            z.writestr(info, content)
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--version", required=True, help="version, e.g. 0.1.0 (a leading v is stripped)")
    ap.add_argument("--only", action="append", choices=sorted(TARGETS), help="build only these targets")
    ap.add_argument("--dist", default=str(ROOT / "dist"), help="output directory")
    args = ap.parse_args()
    version = args.version.lstrip("v")
    dist = pathlib.Path(args.dist)
    bins = dist / "bin"
    bins.mkdir(parents=True, exist_ok=True)
    for key in args.only or list(TARGETS):
        goos, goarch, _ = TARGETS[key]
        exe = build_binary(goos, goarch, version, bins)
        whl = build_wheel(key, version, exe, dist)
        print(f"{key:14} {exe.stat().st_size / 1e6:5.1f} MB  {whl.name}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
