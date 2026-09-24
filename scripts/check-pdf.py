#!/usr/bin/env python3
"""Assert that rendered reports have headers, footers and page numbers.

Usage: check-pdf.py file.pdf [...]. Requires: pip install pymupdf
"""
import re
import sys

import pymupdf

failed = False
for path in sys.argv[1:]:
    doc = pymupdf.open(path)
    has_cover = "reporting period" in doc[0].get_text().lower()
    for i, page in enumerate(doc):
        if has_cover and i == 0:
            continue
        height = page.rect.height
        top = " ".join(b[4] for b in page.get_text("blocks") if b[3] < 60)
        bottom = " ".join(b[4] for b in page.get_text("blocks") if b[1] > height - 45)
        problems = []
        if not top.strip():
            problems.append("no page header")
        if not re.search(r"Page \d+ of \d+", bottom):
            problems.append("no page number")
        if problems:
            failed = True
            print(f"{path} page {i + 1}: {', '.join(problems)}")
    print(f"{path}: {doc.page_count} pages checked")
sys.exit(1 if failed else 0)
