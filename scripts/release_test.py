#!/usr/bin/env python3
import errno
import hashlib
import os
import pathlib
import platform
import re
import stat
import subprocess
import tarfile
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "build-release.sh"
INSTALL_ACTIONLINT = ROOT / "scripts" / "install-actionlint.sh"
SHIP = ROOT / ".github" / "workflows" / "ship.yml"
IMPL = ROOT / ".github" / "workflows" / "release-impl.yml"
CI = ROOT / ".github" / "workflows" / "ci.yml"
README = ROOT / "README.md"
TAP_DISPATCH = (
    "gh workflow run update-formula.yml --repo yashiels/homebrew-tap --ref main "
    "-f formula=amazon -f tag=v0.1.0 -f repository=yashiels/amazon-za-cli "
    "-f description='Read-only Amazon South Africa shopping CLI' "
    "-f artifact_template='amazon_{version}_{target}.tar.gz' -f request_id=UNIQUE_ID"
)


def run(args, env=None, cwd=None):
    return subprocess.run(
        args,
        cwd=cwd or ROOT,
        env=env,
        text=True,
        capture_output=True,
        check=False,
    )


def write_fake_go(path, log_path):
    path.write_text(
        """#!/bin/bash
set -euo pipefail
{
  printf '%s\\n' "$*"
  printf 'CGO_ENABLED=%s GOOS=%s GOARCH=%s\\n' "${CGO_ENABLED:-}" "${GOOS:-}" "${GOARCH:-}"
} >> "$FAKE_GO_LOG"
out=""
prev=""
version=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then
    out=$arg
  fi
  case "$arg" in
    *-X\\ main.version=*)
      version=${arg#*-X main.version=}
      version=${version%% *}
      ;;
  esac
  prev=$arg
done
test -n "$out"
test -n "$version"
mkdir -p "$(dirname "$out")"
cat > "$out" << EOF
#!/bin/bash
log="\\${SYNTHETIC_EXEC_LOG:-}"
if [ -n "\\$log" ]; then
  printf '%s\\\\n' "\\$*" >> "\\$log"
fi
case "\\${1:-}" in
  --version)
    printf '%s\\\\n' "$version"
    ;;
  --help)
    printf 'help\\\\n'
    ;;
  info)
    printf '%s\\\\n' '{"error":"invalid command arguments; run amazon --help","code":"usage"}'
    exit 4
    ;;
esac
EOF
chmod 0755 "$out"
"""
    )
    path.chmod(0o755)
    log_path.write_text("")


class ScriptSafetyTests(unittest.TestCase):
    def test_shebang_and_no_comments(self):
        lines = SCRIPT.read_text().splitlines()
        self.assertEqual(lines[0], "#!/bin/bash")
        for line in lines[1:]:
            self.assertFalse(line.lstrip().startswith("#"))
        self.assertNotIn("rm -rf", "\n".join(lines))
        self.assertNotIn("rm -r", "\n".join(lines))
        syntax = run(["/bin/bash", "-n", str(SCRIPT)])
        self.assertEqual(syntax.returncode, 0, syntax.stderr)
        install_syntax = run(["/bin/bash", "-n", str(INSTALL_ACTIONLINT)])
        self.assertEqual(install_syntax.returncode, 0, install_syntax.stderr)
        self.assertIn('"$raw" = "/"', SCRIPT.read_text())

    def test_rejects_versions_without_calling_go(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            bin_dir, log_path, env = self.fake_env(base)
            out = base / "out"
            out.mkdir()
            (out / "keep.txt").write_text("keep")
            outside = self.sibling_archives(base)
            for bad in ["v0.1.0", "0.1", "0.1.0-rc1", "01.2.3", "1.2.3 ", "0.1.0;rm", "$(id)", ""]:
                result = self.run_contained(base, bad, str(out), env)
                self.assertNotEqual(result.returncode, 0, bad)
                self.assertEqual((out / "keep.txt").read_text(), "keep")
            self.assertEqual(log_path.read_text(), "")
            self.assertFalse(bin_dir.joinpath("unused").exists())
            self.assertEqual(self.sibling_archives(base), outside)

    def test_synthetic_rejects_unsafe_output_without_calling_go(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            _, log_path, env = self.fake_env(base)
            (base / "keep.txt").write_text("keep")
            escape = "escaped-" + base.name
            outside = self.sibling_archives(base)
            unsafe = [
                ".",
                "..",
                f"sub/../../{escape}",
                "out;rm",
                "has space",
                "-out",
                "a//b",
                "",
            ]
            for bad in unsafe:
                before = (base / "keep.txt").read_text()
                result = self.run_contained(base, "0.1.0", bad, env)
                self.assertNotEqual(result.returncode, 0, bad + result.stderr)
                self.assertEqual((base / "keep.txt").read_text(), before)
            self.assertEqual(log_path.read_text(), "")
            self.assertFalse((base.parent / escape).exists())
            self.assertEqual(self.sibling_archives(base), outside)

    def test_synthetic_rejects_symlink_file_and_unowned_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            _, log_path, env = self.fake_env(base)
            outside = self.sibling_archives(base)
            real = base / "real"
            real.mkdir()
            (real / "keep.txt").write_text("ok")
            link = base / "link"
            link.symlink_to(real, target_is_directory=True)
            linked = self.run_contained(base, "0.1.0", str(link), env)
            self.assertNotEqual(linked.returncode, 0, linked.stderr)
            self.assertEqual((real / "keep.txt").read_text(), "ok")
            target = base / "file"
            target.write_text("x")
            filed = self.run_contained(base, "0.1.0", str(target), env)
            self.assertNotEqual(filed.returncode, 0, filed.stderr)
            self.assertEqual(target.read_text(), "x")
            self.assertEqual(log_path.read_text(), "")
            self.assertEqual(self.sibling_archives(base), outside)
            with self.subTest(case="foreign-owner"):
                self.reject_foreign_owner(base, env, log_path)
            self.assertEqual(self.sibling_archives(base), outside)

    def test_synthetic_failure_keeps_preexisting_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            bin_dir = base / "bin"
            bin_dir.mkdir()
            log_path = base / "go.log"
            (bin_dir / "go").write_text(
                "#!/bin/bash\nprintf '%s\\n' \"$*\" >> \"$FAKE_GO_LOG\"\nexit 1\n"
            )
            (bin_dir / "go").chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = str(bin_dir) + os.pathsep + env["PATH"]
            env["FAKE_GO_LOG"] = str(log_path)
            out = base / "out"
            out.mkdir()
            (out / "keep.txt").write_text("keep")
            (out / "custom.tar.gz").write_bytes(b"not-an-archive")
            (out / "checksums.txt").write_text("old\n")
            nested = out / "nested"
            nested.mkdir()
            (nested / "file.txt").write_text("nested")
            result = self.run_contained(base, "9.9.9", str(out), env)
            self.assertNotEqual(result.returncode, 0, result.stderr)
            self.assertEqual((out / "keep.txt").read_text(), "keep")
            self.assertEqual((out / "custom.tar.gz").read_bytes(), b"not-an-archive")
            self.assertEqual((out / "checksums.txt").read_text(), "old\n")
            self.assertEqual((nested / "file.txt").read_text(), "nested")
            self.assertTrue(out.is_dir())
            self.assertIn("build", log_path.read_text())

    def test_synthetic_build_layout_checksums_and_single_exec(self):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            bin_dir = base / "bin"
            bin_dir.mkdir()
            log_path = base / "go.log"
            exec_log = base / "exec.log"
            write_fake_go(bin_dir / "go", log_path)
            env = os.environ.copy()
            env["PATH"] = str(bin_dir) + os.pathsep + env["PATH"]
            env["FAKE_GO_LOG"] = str(log_path)
            env["SYNTHETIC_EXEC_LOG"] = str(exec_log)
            out = base / "out"
            out.mkdir()
            (out / "keep.txt").write_text("keep")
            (out / "custom.tar.gz").write_bytes(b"not-an-archive")
            nested = out / "nested"
            nested.mkdir()
            (nested / "file.txt").write_text("nested")
            sibling = base / "elsewhere"
            sibling.mkdir()
            (sibling / "secret.txt").write_text("secret")
            result = self.run_contained(base, "9.9.9", str(out), env)
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            self.assertEqual((out / "keep.txt").read_text(), "keep")
            self.assertEqual((out / "custom.tar.gz").read_bytes(), b"not-an-archive")
            self.assertEqual((nested / "file.txt").read_text(), "nested")
            self.assertEqual((sibling / "secret.txt").read_text(), "secret")
            names = [
                "amazon_9.9.9_darwin_amd64.tar.gz",
                "amazon_9.9.9_darwin_arm64.tar.gz",
                "amazon_9.9.9_linux_amd64.tar.gz",
                "amazon_9.9.9_linux_arm64.tar.gz",
            ]
            lines = (out / "checksums.txt").read_text().splitlines()
            self.assertEqual([line.split()[1] for line in lines], names)
            for line, name in zip(lines, names):
                digest, seen = line.split()
                blob = (out / name).read_bytes()
                self.assertEqual(hashlib.sha256(blob).hexdigest(), digest)
                self.assertEqual(seen, name)
                with tarfile.open(out / name, "r:gz") as archive:
                    self.assertEqual(archive.getnames(), ["amazon"])
                    member = archive.getmember("amazon")
                    self.assertTrue(member.isfile())
                    self.assertEqual(stat.S_IMODE(member.mode), 0o755)
            go_log = log_path.read_text()
            for pair in ("GOOS=darwin GOARCH=arm64", "GOOS=darwin GOARCH=amd64", "GOOS=linux GOARCH=arm64", "GOOS=linux GOARCH=amd64"):
                self.assertEqual(go_log.count(pair), 1, go_log)
            self.assertEqual(go_log.count("CGO_ENABLED=0"), 4)
            self.assertIn("-trimpath", go_log)
            self.assertIn("-s -w -X main.version=9.9.9", go_log)
            self.assertIn("./cmd/amazon", go_log)
            executions = [line for line in exec_log.read_text().splitlines() if line]
            self.assertEqual(
                executions,
                ["--version", "--help", "info https://example.com/dp/B0H471LF7H --json"],
            )

    def sibling_archives(self, root):
        return {
            path
            for path in root.parent.iterdir()
            if path.is_file() and path.name.startswith("amazon_") and path.name.endswith(".tar.gz")
        }

    def confined(self, root, output):
        if not output.startswith("/"):
            return
        root = pathlib.Path(root).resolve()
        path = pathlib.Path(output)
        parent = path.parent.resolve()
        inside = parent == root or root in parent.parents
        self.assertTrue(inside, output)

    def run_contained(self, root, version, output, env):
        self.confined(root, output)
        before = self.sibling_archives(root)
        result = run(["/bin/bash", str(SCRIPT), version, output], env=env, cwd=root)
        self.assertEqual(self.sibling_archives(root), before)
        return result

    def reject_foreign_owner(self, base, env, log_path):
        foreign = base / "foreign"
        foreign.mkdir()
        (foreign / "keep.txt").write_text("keep")
        uid = foreign.stat().st_uid
        gid = foreign.stat().st_gid
        try:
            try:
                os.chown(foreign, 65534, 65534)
            except OSError as exc:
                if exc.errno not in (errno.EPERM, errno.EACCES):
                    raise
                self.skipTest("no safely creatable different-UID target")
            if foreign.stat().st_uid == os.getuid():
                self.skipTest("no safely creatable different-UID target")
            result = self.run_contained(base, "0.1.0", str(foreign), env)
            self.assertNotEqual(result.returncode, 0, result.stderr)
            self.assertEqual((foreign / "keep.txt").read_text(), "keep")
            self.assertEqual(list(foreign.glob("amazon_*.tar.gz")), [])
            self.assertFalse((foreign / "checksums.txt").exists())
            self.assertEqual(log_path.read_text(), "")
        finally:
            if foreign.stat().st_uid != uid or foreign.stat().st_gid != gid:
                os.chown(foreign, uid, gid)

    def fake_env(self, base):
        bin_dir = base / "bin"
        bin_dir.mkdir()
        log_path = base / "go.log"
        write_fake_go(bin_dir / "go", log_path)
        env = os.environ.copy()
        env["PATH"] = str(bin_dir) + os.pathsep + env["PATH"]
        env["FAKE_GO_LOG"] = str(log_path)
        env["SYNTHETIC_EXEC_LOG"] = str(base / "exec.log")
        return bin_dir, log_path, env


class WorkflowTests(unittest.TestCase):
    def test_publish_is_opt_in_and_targets_exact_sha(self):
        ship = SHIP.read_text()
        impl = IMPL.read_text()
        self.assertIn("workflow_dispatch:", self.trigger_block(ship))
        self.assertNotIn("push", self.trigger_block(ship))
        self.assertNotIn("pull_request", self.trigger_block(ship))
        self.assertNotIn("tags", self.trigger_block(ship))
        self.assertIn("workflow_call:", self.trigger_block(impl))
        self.assertNotIn("workflow_dispatch:", self.trigger_block(impl))
        self.assertNotIn("push", self.trigger_block(impl))
        self.assertNotIn("pull_request", self.trigger_block(impl))
        self.assertIn('default: "0.1.0"', self.input_block(ship, "version"))
        self.assertIn("default: true", self.input_block(ship, "dry_run"))
        self.assertIn("type: boolean", self.input_block(ship, "dry_run"))
        self.assertIn("source_sha: ${{ github.sha }}", ship)
        self.assertIn("ref: ${{ inputs.source_sha }}", impl)
        self.assertIn('go-version: "1.27.2"', impl)
        self.assertIn("inputs.dry_run == false", impl)
        self.assertIn('case "$DRY_RUN" in\n            false)', impl)
        self.assertIn('gh release create "$tag"', impl)
        self.assertIn('--target "$SOURCE_SHA"', impl)
        self.assertNotIn("--verify-tag", impl)
        self.assertNotIn("gh release delete", impl)
        self.assertNotIn("gh release upload", impl)
        self.assertNotIn("git tag", impl)
        self.assertNotIn("git push", impl)
        self.assertNotIn("gh release create", ship)
        self.assertIn("cancel-in-progress: false", ship)
        self.assertIn("refs/heads/main", ship)
        self.assertIn("refs/heads/main", impl)
        self.assertIn("contents: write", impl)
        self.assertIn("github.token", impl)
        self.assertNotIn("secrets.", ship + impl)
        self.assertNotIn("version.env", impl.replace("version.env is not modified.", ""))
        self.assertIn("version.env is not modified.", impl)
        self.assertIn("upload-artifact", impl)
        self.assertIn("checksums.txt", impl)
        allowed = re.compile(
            r"^\s+(RELEASE_VERSION|SOURCE_SHA|DRY_RUN|version|dry_run|source_sha|ref|if):\s"
        )
        for path in (SHIP, IMPL):
            for line in path.read_text().splitlines():
                if "${{ inputs." in line:
                    self.assertRegex(line, allowed)

    def test_ci_runs_release_tests_and_actionlint_without_weakening_lint(self):
        ci = CI.read_text()
        self.assertIn("python3 scripts/release_test.py", ci)
        self.assertIn("scripts/install-actionlint.sh", ci)
        self.assertIn("actionlint", ci)
        self.assertIn("scripts/install-actionlint.sh", IMPL.read_text())
        self.assertIn("version=1.7.12", INSTALL_ACTIONLINT.read_text())
        self.assertIn("golangci/golangci-lint-action@4afd733a84b1f43292c63897423277bb7f4313a9", ci)
        self.assertIn("version: v2.15.0", ci)
        self.assertIn("TZ=UTC go test ./...", ci)
        self.assertIn("make build", ci)

    def test_readme_documents_dry_run_publish_and_tap_dispatch(self):
        readme = README.read_text()
        self.assertIn("gh workflow run ship.yml --ref main -f version=0.1.0 -f dry_run=true", readme)
        self.assertIn("gh workflow run ship.yml --ref main -f version=0.1.0 -f dry_run=false", readme)
        self.assertIn(TAP_DISPATCH, readme)
        self.assertIn("version.env", readme)
        self.assertIn("does not edit or commit `version.env`", readme)

    def trigger_block(self, text):
        start = text.index("on:\n")
        end = text.index("\npermissions:", start)
        return text[start:end]

    def input_block(self, text, name):
        marker = f"\n      {name}:"
        start = text.index(marker)
        lines = []
        for line in text[start + 1 :].splitlines():
            if lines and line.startswith("      ") and not line.startswith("        "):
                break
            lines.append(line)
        return "\n".join(lines)


class RealReleaseBuildTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.out = pathlib.Path(cls.tmp.name) / "out"
        cls.out.mkdir()
        (cls.out / "keep.txt").write_text("keep\n")
        nested = cls.out / "nested"
        nested.mkdir()
        (nested / "file.txt").write_text("nested\n")
        (cls.out / "custom.tar.gz").write_bytes(b"not-an-archive")
        cls.version_env = (ROOT / "version.env").read_bytes()
        completed = run(["/bin/bash", str(SCRIPT), "0.1.0", str(cls.out)])
        if completed.returncode != 0:
            raise AssertionError(completed.stdout + "\n" + completed.stderr)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def test_preexisting_files_and_version_env_remain(self):
        self.assertEqual((self.out / "keep.txt").read_text(), "keep\n")
        self.assertEqual((self.out / "nested" / "file.txt").read_text(), "nested\n")
        self.assertEqual((self.out / "custom.tar.gz").read_bytes(), b"not-an-archive")
        self.assertEqual((ROOT / "version.env").read_bytes(), self.version_env)

    def test_archive_layout_and_checksums(self):
        expected = [
            "amazon_0.1.0_darwin_amd64.tar.gz",
            "amazon_0.1.0_darwin_arm64.tar.gz",
            "amazon_0.1.0_linux_amd64.tar.gz",
            "amazon_0.1.0_linux_arm64.tar.gz",
        ]
        lines = (self.out / "checksums.txt").read_text().splitlines()
        self.assertEqual(len(lines), 4)
        seen = []
        for line in lines:
            digest, name = line.split()
            seen.append(name)
            blob = (self.out / name).read_bytes()
            self.assertEqual(hashlib.sha256(blob).hexdigest(), digest)
            with tarfile.open(self.out / name, "r:gz") as archive:
                self.assertEqual(archive.getnames(), ["amazon"])
                member = archive.getmember("amazon")
                self.assertTrue(member.isfile())
                self.assertEqual(stat.S_IMODE(member.mode), 0o755)
        self.assertEqual(seen, expected)

    def test_host_archive_smoke(self):
        os_name = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
        arch = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64", "amd64": "amd64"}[platform.machine()]
        archive = self.out / f"amazon_0.1.0_{os_name}_{arch}.tar.gz"
        extract = pathlib.Path(self.tmp.name) / "extract"
        extract.mkdir()
        with tarfile.open(archive, "r:gz") as opened:
            try:
                opened.extractall(extract, filter="data")
            except TypeError:
                opened.extractall(extract)
        binary = extract / "amazon"
        version = run([str(binary), "--version"])
        self.assertEqual(version.returncode, 0, version.stderr)
        self.assertEqual(version.stdout, "0.1.0\n")
        help_run = run([str(binary), "--help"])
        self.assertEqual(help_run.returncode, 0, help_run.stderr)
        self.assertTrue(help_run.stdout.strip())
        env = os.environ.copy()
        env.update(
            {
                "HTTP_PROXY": "http://127.0.0.1:9",
                "HTTPS_PROXY": "http://127.0.0.1:9",
                "ALL_PROXY": "http://127.0.0.1:9",
            }
        )
        info = run(
            [str(binary), "info", "https://example.com/dp/B0H471LF7H", "--json"],
            env=env,
        )
        self.assertEqual(info.returncode, 4, info.stdout + info.stderr)
        self.assertIn('"code":"usage"', info.stdout)


if __name__ == "__main__":
    unittest.main()
