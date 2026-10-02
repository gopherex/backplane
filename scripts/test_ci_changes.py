import unittest

from ci_changes import classify


class ChangeSelectionTest(unittest.TestCase):
    def selected(self, *paths):
        return {key for key, value in classify(paths).items() if value}

    def test_docs_do_not_run_application_suites(self):
        self.assertEqual(self.selected("docs/development.md", "web/packages/ui/README.md",
                                        "website/docusaurus.config.js", "website/yarn.lock",
                                        ".github/workflows/docs.yml"), set())

    def test_backend_does_not_run_frontend(self):
        self.assertEqual(self.selected("internal/ops/schedules.go", "go.sum"), {"go"})

    def test_frontend_does_not_run_backend(self):
        self.assertEqual(self.selected("web/packages/ui/src/button.tsx", "web/yarn.lock"), {"web"})

    def test_contracts_run_both_consumers_and_generation(self):
        for path in ("backplanepb/v1/service.proto", "web/packages/api/src/gen/service_pb.ts",
                     "internal/store/schema.sql", "examples/hello/easyp.yaml"):
            with self.subTest(path=path):
                self.assertEqual(self.selected(path), {"contracts", "go", "web"})

    def test_embedded_assets_run_backend(self):
        self.assertEqual(self.selected("internal/console/assets/login.html"), {"go"})

    def test_compose_does_not_start_application_suites(self):
        self.assertEqual(self.selected("docker-compose.minimal.yaml"), {"compose"})

    def test_release_packaging_does_not_retest_unchanged_go(self):
        self.assertEqual(self.selected(".github/workflows/release.yml", "scripts/build-release.sh",
                                      "scripts/release-notes.py", ".dockerignore", "deployments/Dockerfile"), {"compose"})
        self.assertEqual(self.selected("README.md", ".github/assets/banner.svg", ".github/assets/console.png"), set())

    def test_unknown_and_ci_changes_fail_closed(self):
        for path in ("Makefile", ".github/workflows/ci.yml", "scripts/ci_changes.py", "new.config"):
            with self.subTest(path=path):
                self.assertEqual(self.selected(path), {"go", "web", "contracts", "compose"})

    def test_multiple_changes_combine_suites(self):
        self.assertEqual(self.selected("pkg/backplane/server.go", "web/tests/foo.unit.ts"), {"go", "web"})


if __name__ == "__main__":
    unittest.main()
