import unittest

from rtcproof import census_complete


class CensusVerdictTests(unittest.TestCase):
    def test_requires_rate_limit_closure(self):
        self.assertFalse(census_complete([b"a"], [b"a"], False))

    def test_accepts_unordered_exact_datagrams(self):
        self.assertTrue(census_complete([b"a", b"b", b"a"], [b"b", b"a", b"a"], True))

    def test_rejects_missing_duplicate_or_modified_datagrams(self):
        for received in ([b"a"], [b"a", b"a"], [b"a", b"c"]):
            with self.subTest(received=received):
                self.assertFalse(census_complete([b"a", b"b"], received, True))


if __name__ == "__main__":
    unittest.main()
