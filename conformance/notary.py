#!/usr/bin/env python3
"""Real gsp CLI ↔ swarmidx HTTP/SQLite conformance, entirely local.

Requires a built gsp and Python with swarmidx/backend/requirements.txt installed.
No existing database, notary, git remote or operator key is used. The test runner
creates/destroys its own in-memory SQLite database and loopback HTTP listener.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--swarmidx", required=True, type=Path)
    parser.add_argument("--gsp", required=True, type=Path)
    parser.add_argument("--genswarms", type=Path, help="also verify package execution and database-only restart")
    args = parser.parse_args()
    binary = args.gsp.resolve(strict=True)
    sys.path.insert(0, str(args.swarmidx.resolve(strict=True) / "backend"))
    os.environ["DJANGO_SETTINGS_MODULE"] = "config.settings.dev"

    import django
    from django.conf import settings

    # Override before django.setup(): never select an operator's configured DB.
    settings.DATABASES = {"default": {
        "ENGINE": "django.db.backends.sqlite3", "NAME": ":memory:",
    }}
    settings.SWARMIDX_LOG_SIGNING_KEY = "00" * 32  # Public fixture seed only.
    settings.SECRET_KEY = "public-conformance-fixture"
    settings.ALLOWED_HOSTS = ["127.0.0.1", "localhost", "testserver"]
    django.setup()

    from django.contrib.auth import get_user_model
    from django.test import LiveServerTestCase
    from django.test.runner import DiscoverRunner
    from accounts.services import current_scope, mint_publish_token
    from registry import services, transparency
    from registry.models import LogEntry, Release

    class NotaryConformance(LiveServerTestCase):
        host = "127.0.0.1"

        def setUp(self):
            temporary = tempfile.TemporaryDirectory(prefix="gsp-notary-conformance-")
            self.addCleanup(temporary.cleanup)
            self.root = Path(temporary.name)
            local_source = self.settings(SWARMIDX_LOCAL_SOURCE_ROOT=str(self.root))
            local_source.enable()
            self.addCleanup(local_source.disable)
            self.user = get_user_model().objects.create_user(username="fixture")
            self.scope = current_scope(self.user)
            _, self.token = mint_publish_token(self.scope, "local-conformance")
            packages = []
            for name, kind, directory in (
                ("policy", "policy", "policy"),
                ("body", "body", "café🚀"),
                ("handler", "handler", "handler"),
            ):
                path = self.root / directory
                path.mkdir()
                (path / "fixture.txt").write_text(f"{kind} fixture\n", encoding="utf-8")
                if name == "body":
                    (path / "body.md").write_text("Verified café {{agent_name}}", encoding="utf-8")
                elif name == "policy":
                    (path / "policy.json").write_text('{"fixture":"notary-policy"}')
                elif name == "handler":
                    (path / "swarm-object.json").write_text(json.dumps({"module": "NotaryRuntimeFixture", "files": ["object.ex"]}))
                    (path / "object.ex").write_text('''defmodule NotaryRuntimeFixture do
  def init(config) do
    File.write!(config["receipt"], "verified-handler")
    {:ok, config}
  end
  def handle_message(_, _, state), do: {:noreply, state}
  def interface, do: %{}
end
''')
                package = {"name": name, "kind": kind, "dir": directory}
                if name == "body":
                    package["deps"] = ["fixture/policy@1"]
                packages.append(package)
            self.manifest = self.root / "swarmidx.json"
            self.manifest.write_text(json.dumps({
                "registry": {"scope": "fixture"}, "packages": packages,
            }), encoding="utf-8")
            # The real CLI computes claims; the real API authenticates the token,
            # hashes local package bytes independently, persists and signs them.
            self.gsp("publish", str(self.manifest), "--version", "1",
                     "--source", "local:" + str(self.root), publish=True)

        def gsp(self, *args, publish=False, success=True, key=None, local=True):
            env = {"PATH": os.environ.get("PATH", ""), "HOME": str(self.root),
                   "LANG": "C.UTF-8", "SWARMIDX_ENDPOINT": self.live_server_url,
                   "SWARMIDX_PUBLIC_KEY": transparency.public_key_hex() if key is None else key}
            if publish:
                env["SWARMIDX_TOKEN"] = self.token
            if args[0] == "vendor" and local:
                args = (args[0], "--local-source-root", str(self.root), *args[1:])
            result = subprocess.run([str(binary), *args], cwd=self.root, env=env,
                                    capture_output=True, text=True, timeout=30)
            if success:
                self.assertEqual(result.returncode, 0, result.stderr)
            else:
                self.assertNotEqual(result.returncode, 0, "unexpected success")
            return result

        @unittest.skipUnless(args.genswarms, "pass --genswarms for package-to-runtime/database conformance")
        def test_verified_packages_execute_and_restore_without_seed_file(self):
            refs = ["swarmidx:fixture/" + name + "@1" for name in ("body", "policy", "handler")]
            self.gsp("vendor", "--dir", "vendor", *refs)
            entries = json.loads((self.root / "vendor/vendor-lock.json").read_text())["entries"]
            def slot(name, kind):
                entry = next(e for e in entries if e["ref"] == "swarmidx:fixture/" + name + "@1")
                return {"ref": entry["ref"], "digest": entry["digest"], "kind": kind,
                        "opts": {"path": str(self.root / "vendor" / entry["path"])}}
            handler = slot("handler", "code")
            handler["opts"]["mode"] = "require"
            seed = {"v": 1, "kind": "swarm.state", "name": "notary-runtime", "phase": "desired",
                    "agents": [{"name": "worker", "body": slot("body", "data"), "model": {"policy": slot("policy", "data")},
                                "backend": {"ref": "mock"}, "overrides": {}, "config": {}}],
                    "objects": [{"name": "board", "handler": handler, "config": {"receipt": str(self.root / "receipt")}}],
                    "topology": [["worker", "board"]], "options": {}}
            seed_path = self.root / "native.json"
            seed_path.write_text(json.dumps(seed))
            self.gsp("verify", str(seed_path))
            script = r'''
            [root, phase] = System.argv()
            Application.put_env(:genswarms, :db_path, Path.join(root, "runtime.db"))
            Application.put_env(:genswarms, :events_dir, Path.join(root, "events"))
            Application.put_env(:genswarms, :swarm_data_dir, Path.join(root, "runtime"))
            Application.put_env(:genswarms, :load_dotenv, false)
            {:ok, _} = Application.ensure_all_started(:genswarms)
            result = if phase == "write" do
              root |> Path.join("native.json") |> File.read!() |> Jason.decode!() |> Genswarms.start_swarm_from_ir()
            else
              Genswarms.restore_swarm("notary-runtime")
            end
            {:ok, "notary-runtime"} = result
            "verified-handler" = File.read!(Path.join(root, "receipt"))
            [{pid, _}] = Registry.lookup(Genswarms.AgentRegistry, {"notary-runtime", :worker})
            state = :sys.get_state(pid)
            %{"policy_ir" => %{"fixture" => "notary-policy"}} = state.backend_config.request_extra
            "Verified café worker" = File.read!(Path.join(state.skills_dir, "package-body.md"))
            {:ok, nil} = Genswarms.stop_swarm("notary-runtime")
            IO.puts("VERIFIED_RUNTIME_OK")
            '''
            for phase in ("write", "restore"):
                result = subprocess.run(["mix", "run", "--no-start", "-e", script, "--", str(self.root), phase],
                    cwd=args.genswarms, env={"PATH": os.environ.get("PATH", ""), "HOME": str(self.root), "MIX_ENV": "test", "LANG": "C.UTF-8",
                        "MIX_HOME": os.environ.get("MIX_HOME", str(Path.home() / ".mix"))},
                    capture_output=True, text=True, timeout=60)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn("VERIFIED_RUNTIME_OK", result.stdout)
                if phase == "write":
                    seed_path.unlink()
                    (self.root / "receipt").unlink()

        def seed(self, digest=None, body_ref="swarmidx:fixture/body@1"):
            body = {"ref": body_ref, "kind": "data"}
            if digest is not None:
                body["digest"] = digest
            state = {"v": 1, "kind": "swarm.state", "name": "fixture", "phase": "desired",
                     "agents": [{"name": "worker", "body": body,
                                 "model": {"policy": {"ref": "swarmidx:fixture/policy@1", "kind": "data"}},
                                 "backend": {"ref": "mock"}}],
                     "objects": [{"name": "board", "handler": {
                         "ref": "swarmidx:fixture/handler@1", "kind": "code"}}],
                     "topology": [["worker", "board"]]}
            path = self.root / "seed.json"
            path.write_text(json.dumps(state), encoding="utf-8")
            return path

        def test_publish_resolve_materialize_vendor_and_rehash(self):
            out = json.loads(self.gsp("resolve", "swarmidx:fixture/body@1", "--json").stdout)
            signed = LogEntry.objects.get(payload__ref="swarmidx:fixture/body@1").payload
            self.assertEqual(out["digest"], signed["digest"])
            self.assertEqual(out["deps"], ["fixture/policy@1"])
            materialized = json.loads(self.gsp("materialize", "--resolve", str(self.seed())).stdout)
            self.assertEqual(materialized["agents"][0]["body"]["digest"], signed["digest"])
            materialized_path = self.root / "materialized.json"
            materialized_path.write_text(json.dumps(materialized), encoding="utf-8")
            vendor = self.root / "vendor"
            self.gsp("vendor", "--dir", str(vendor), str(materialized_path))
            lock = json.loads((vendor / "vendor-lock.json").read_text())
            self.assertEqual(len(lock["entries"]), 3)
            for entry in lock["entries"]:
                digest = self.gsp("dirhash", str(vendor / entry["path"])).stdout.strip()
                self.assertEqual(digest, entry["digest"])
            # Dependency traversal also works without an IR listing the policy.
            direct_vendor = self.root / "direct-vendor"
            self.gsp("vendor", "--dir", str(direct_vendor), "swarmidx:fixture/body@1")
            self.assertEqual(len(json.loads((direct_vendor / "vendor-lock.json").read_text())["entries"]), 2)

        def test_unsigned_resolution_metadata_cannot_redirect_installation(self):
            signed = LogEntry.objects.get(payload__ref="swarmidx:fixture/body@1").payload
            Release.objects.filter(package__name="body").update(
                digest="sha256:" + "f" * 64, source="local:/must-not-read", dir="elsewhere")
            api = self.client.get("/v1/resolve", {"ref": "swarmidx:fixture/body@1"}).json()
            self.assertNotEqual(api["digest"], signed["digest"])
            resolved = json.loads(self.gsp("resolve", "swarmidx:fixture/body@1", "--json").stdout)
            self.assertEqual(resolved["digest"], signed["digest"])
            self.gsp("vendor", "--dir", str(self.root / "vendor"), "swarmidx:fixture/body@1")

        def test_bad_pins_and_slot_kinds_fail_before_vendoring(self):
            path = self.seed(digest="sha256:" + "f" * 64)
            self.gsp("materialize", "--resolve", str(path), success=False)
            self.gsp("vendor", "--dir", str(self.root / "vendor"), str(path), success=False)
            wrong_kind = self.seed(body_ref="swarmidx:fixture/policy@1")
            self.gsp("materialize", "--resolve", str(wrong_kind), success=False)
            self.gsp("vendor", "--dir", str(self.root / "vendor"), str(wrong_kind), success=False)
            self.assertFalse((self.root / "vendor").exists())

        def test_offline_materialize_needs_no_key_and_preserves_authored_state(self):
            path = self.seed()
            output = json.loads(self.gsp("materialize", str(path), key="").stdout)
            self.assertNotIn("digest", output["agents"][0]["body"])
            self.assertEqual(output["agents"][0]["backend"]["ref"], "mock")

        def test_wrong_key_and_tampered_log_fail_before_writes(self):
            self.gsp("vendor", "--dir", str(self.root / "vendor"),
                     "swarmidx:fixture/body@1", key="ff" * 32, success=False)
            entry = LogEntry.objects.get(payload__ref="swarmidx:fixture/body@1")
            entry.payload = {**entry.payload, "digest": "sha256:" + "f" * 64}
            entry.save(update_fields=["payload"])
            self.gsp("vendor", "--dir", str(self.root / "vendor"),
                     "swarmidx:fixture/body@1", success=False)
            self.assertFalse((self.root / "vendor").exists())

        def test_signed_withdrawal_prevents_resolution(self):
            services.delete_package(self.user, "fixture", "body", "fixture/body")
            self.gsp("resolve", "swarmidx:fixture/body@1", success=False)
            self.gsp("resolve", "swarmidx:fixture/policy@1")

        def test_local_sources_need_separate_host_path_authority(self):
            result = self.gsp("vendor", "--dir", str(self.root / "vendor"),
                              "swarmidx:fixture/body@1", local=False, success=False)
            self.assertIn("local-source-root", result.stderr)
            self.assertFalse((self.root / "vendor" / "fixture__body@1").exists())

        def test_modified_existing_package_is_preserved(self):
            vendor = self.root / "vendor"
            self.gsp("vendor", "--dir", str(vendor), "swarmidx:fixture/body@1")
            modified = vendor / "fixture__body@1" / "fixture.txt"
            modified.write_text("operator edits\n", encoding="utf-8")
            lock = (vendor / "vendor-lock.json").read_bytes()
            self.gsp("vendor", "--dir", str(vendor), "swarmidx:fixture/body@1", success=False)
            self.assertEqual(modified.read_text(), "operator edits\n")
            self.assertEqual((vendor / "vendor-lock.json").read_bytes(), lock)

    runner = DiscoverRunner(verbosity=2, interactive=False)
    runner.setup_test_environment()
    old_config = runner.setup_databases()
    try:
        suite = unittest.defaultTestLoader.loadTestsFromTestCase(NotaryConformance)
        result = runner.run_suite(suite)
    finally:
        runner.teardown_databases(old_config)
        runner.teardown_test_environment()
    return 0 if result.wasSuccessful() else 1


if __name__ == "__main__":
    sys.exit(main())
