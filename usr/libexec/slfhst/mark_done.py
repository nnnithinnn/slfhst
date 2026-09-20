#!/usr/bin/python3
"""Marks a stage complete: usage: mark_done.py stage1 | mark_done.py stage2"""
import sys

from slfhst import common

if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: mark_done.py <stage1|stage2>")
    common.mark_done(sys.argv[1])
