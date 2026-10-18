#!/usr/bin/env python3
import subprocess
import sys
from pathlib import Path

for generator in sorted((Path(__file__).resolve().parent / "corpora").glob("gen_*.py")):
    subprocess.run([sys.executable, str(generator), *sys.argv[1:]], check=True)
