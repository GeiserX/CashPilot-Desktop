#!/usr/bin/env python3
"""Lab only: print the /etc/kcpassword bytes for a password, in hex.

macOS autologin reads /etc/kcpassword: the password XORed with a fixed 11-byte
key and padded to a multiple of 12 bytes. It is an obfuscation, not a
cipher: anyone who can read the file (root only, mode 600) gets the password.

Reads the password from stdin so it never shows in `ps`.
"""
import sys

KEY = [0x7D, 0x89, 0x52, 0x23, 0xD2, 0xBC, 0xDD, 0xEA, 0xA3, 0xB9, 0x1F]

pw = sys.stdin.readline().rstrip("\n").encode()
if len(pw) % 12:
    pw += b"\0" * (12 - len(pw) % 12)
print(bytes(b ^ KEY[i % len(KEY)] for i, b in enumerate(pw)).hex())
