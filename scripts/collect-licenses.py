#!/usr/bin/env python3
import importlib.metadata
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
root=Path(__file__).resolve().parent.parent
target=Path(sys.argv[1]) / 'licenses'
target.mkdir(parents=True,exist_ok=True)
for name in ['PySide6','PySide6_Essentials','PySide6_Addons','shiboken6','PyInstaller','dbus-next']:
    dist=importlib.metadata.distribution(name)
    for file in dist.files or []:
        if 'license' in str(file).lower() or 'copying' in str(file).lower():
            src=Path(dist.locate_file(file))
            if src.is_file():
                dest=target / name / str(file)
                dest.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(src,dest)
go=os.environ.get('GO','go')
output=subprocess.check_output([go,'list','-m','-json','all'],cwd=root/'backend').decode()
decoder=json.JSONDecoder();pos=0
while pos<len(output):
    while pos<len(output) and output[pos].isspace():pos+=1
    if pos>=len(output):break
    record,n=decoder.raw_decode(output,pos);pos=n
    directory=record.get('Dir')
    if not directory:continue
    for src in Path(directory).iterdir():
        if src.is_file() and src.name.lower().startswith(('license','copying','notice')):
            dest=target/'go'/record['Path']/record.get('Version','local')/src.name
            dest.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(src,dest)
