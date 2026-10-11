#!/usr/bin/env python3
"""Render the original SVG icon into launcher PNGs and the Windows ICO."""
import os
os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")
from pathlib import Path
from PySide6.QtWidgets import QApplication
from PySide6.QtSvg import QSvgRenderer
from PySide6.QtGui import QImage,QPainter
from PySide6.QtCore import Qt
from PIL import Image
root = Path(__file__).resolve().parent.parent
app = QApplication([])
renderer = QSvgRenderer(str(root / "assets/handoff.svg"))
for size in (16,24,32,48,64,128,256):
    image = QImage(size,size,QImage.Format_ARGB32)
    image.fill(Qt.transparent)
    painter=QPainter(image);renderer.render(painter);painter.end()
    image.save(str(root / f"assets/handoff-{size}.png"))
Image.open(root / "assets/handoff-256.png").save(root / "assets/handoff.ico", sizes=[(16,16),(24,24),(32,32),(48,48),(64,64),(128,128),(256,256)])
