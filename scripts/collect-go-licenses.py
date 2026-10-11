#!/usr/bin/env python3
import json,os,subprocess,sys,shutil
from pathlib import Path
root=Path(__file__).resolve().parent.parent
output=subprocess.check_output([os.environ.get('GO','go'),'list','-m','-json','all'],cwd=root/'backend').decode()
decoder=json.JSONDecoder();pos=0
while pos<len(output):
    while pos<len(output) and output[pos].isspace():pos+=1
    if pos>=len(output):break
    module,pos=decoder.raw_decode(output,pos)
    if not module.get('Dir'):continue
    for src in Path(module['Dir']).iterdir():
        if src.is_file() and src.name.lower().startswith(('license','copying','notice')):
            dst=Path(sys.argv[1])/module['Path']/module.get('Version','local')/src.name
            dst.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(src,dst)
