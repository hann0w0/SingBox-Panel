#!/usr/bin/env python3
"""Check smoke-test command parsing without Docker or installing an Agent."""

import importlib.util
from pathlib import Path
import shlex
import unittest


SCRIPT = Path(__file__).resolve().with_name("docker-smoke.py")
SPEC = importlib.util.spec_from_file_location("docker_smoke", SCRIPT)
SMOKE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SMOKE)
CODE = "0123456789abcdef" * 3


class RegistrationCodeTest(unittest.TestCase):
    def test_legacy_pipeline(self):
        command = f"curl -fsSL 'https://panel.example.test/api/agent/install.sh' | sudo bash -s -- --url 'https://panel.example.test' --code '{CODE}'"
        self.assertEqual(SMOKE.registration_code(command), CODE)

    def test_short_bootstrap_with_single_registration_code(self):
        # The panel emits one line; curl/wget and sudo/doas are resolved inside
        # the served script, so the code appears exactly once.
        url = shlex.quote("https://panel.example.test")
        script = shlex.quote("https://panel.example.test/api/agent/install.sh")
        args = f"--url {url} --code '{CODE}'"
        command = f'{{ curl -fsSL {script} || wget -qO- {script}; }} | sh -s -- {args}'
        words = shlex.split(command)
        self.assertEqual(words[words.index("--code") + 1], CODE)
        self.assertEqual(command.count(CODE), 1)
        self.assertEqual(SMOKE.registration_code(command), CODE)

    def test_adjacent_shell_operators(self):
        for operator in (";", ";\n", "&&", "||", "|"):
            with self.subTest(operator=operator):
                self.assertEqual(SMOKE.registration_code(f"sh -s -- --code '{CODE}'{operator}true"), CODE)

    def test_quoted_url_is_one_argument(self):
        url = shlex.quote("https://panel.example.test/path's?a=1;b=2&next=--code")
        self.assertEqual(SMOKE.registration_code(f"sh -s -- --url {url} --code '{CODE}';"), CODE)

    def test_missing_or_invalid_code_is_rejected(self):
        for command in ("", "sh -s -- --url https://panel.example.test", "sh --code", "sh --code; true",
                        "sh --code ''", "sh --code 'not-hex'", f"sh --code '{CODE};'"):
            with self.subTest(command=command):
                with self.assertRaises(ValueError):
                    SMOKE.registration_code(command)

    def test_different_branch_codes_are_rejected(self):
        with self.assertRaises(ValueError):
            SMOKE.registration_code(f"if true; then sh --code '{CODE}'; else sh --code '{'f' * 48}'; fi")

    def test_missing_code_in_later_branch_is_rejected(self):
        with self.assertRaises(ValueError):
            SMOKE.registration_code(f"if true; then sh --code '{CODE}'; else sh --code; fi")

    def test_malformed_quoting_is_rejected(self):
        with self.assertRaises(ValueError):
            SMOKE.registration_code("sh --code 'unterminated")


if __name__ == "__main__":
    unittest.main()
