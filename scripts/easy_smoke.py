"""Exercise portable configuration, console setup and pairing without transfers."""
import base64
import json
import os
from pathlib import Path
import pty
import select
import shutil
import socket
import subprocess
import tempfile
import time

ROOT=Path(__file__).resolve().parents[1]
BIN=ROOT/"bin"
checks=[]
processes=[]
handles=[]
env={k:v for k,v in os.environ.items() if k.upper() not in {"HTTP_PROXY","HTTPS_PROXY","ALL_PROXY","NO_PROXY"}}
env["NO_PROXY"]="*"

def run(program,args,work,extra=None,input=None,expected=0):
    result=subprocess.run([str(program),*map(str,args)],cwd=work,env=extra or env,input=input,capture_output=True,text=True,timeout=30)
    assert result.returncode==expected,(args[0],result.returncode,result.stdout[:300],result.stderr[:300])
    return result.stdout.strip()

with tempfile.TemporaryDirectory(prefix="sitedeck-easy-") as temporary:
    root=Path(temporary)
    try:
        run(BIN/"site-hub",["init"],root)
        secret=(root/".local/hub.key").read_text().strip()
        with socket.socket() as probe:
            probe.bind(("127.0.0.1",0));port=probe.getsockname()[1]
        endpoint=f"ws://127.0.0.1:{port}/ws"
        log=(root/"hub.log").open("w");handles.append(log)
        hub=subprocess.Popen([str(BIN/"site-hub"),"serve","--listen",f"127.0.0.1:{port}"],cwd=root,env=env,stdout=log,stderr=log);processes.append(hub)

        def device(name,configured=True):
            directory=root/name;directory.mkdir()
            program=directory/"site-agent";shutil.copyfile(BIN/"site-agent",program);program.chmod(0o755)
            config={"hub":endpoint,"registrationToken":secret,"permissions":["host"],"autoStart":True}
            if configured:
                (directory/"site-agent.json").write_text(json.dumps(config));(directory/"site-agent.json").chmod(0o600)
            child_env=dict(env,XDG_CONFIG_HOME=str(directory/"private"))
            return directory,program,child_env

        a,agent_a,env_a=device("one")
        log=(a/"run.log").open("w");handles.append(log)
        process=subprocess.Popen([str(agent_a)],cwd=root,env=env_a,stdout=log,stderr=log);processes.append(process)
        deadline=time.monotonic()+8
        state_a=a/"private/SiteDeck/Agent/agent.json"
        while not state_a.exists():
            assert time.monotonic()<deadline and process.poll() is None
            time.sleep(.05)
        text=run(agent_a,["pair","--plain"],root,env_a)
        assert text.startswith("SITEPAIR1.")
        result=json.loads(run(BIN/"site-cli",["--pair-text","-","--transport","relay","host","exec","--command","printf EASY_PAIR_OK"],root,input=text+"\n"))
        assert result["stdout"]=="EASY_PAIR_OK"
        checks.append("no-argument Agent reads executable-adjacent config from another working directory")
        checks.append("first start creates per-device identity without running init")
        checks.append("pairing text from headless Agent directly executes authenticated CLI command")

        profile=root/"imported.json"
        result=json.loads(run(BIN/"site-cli",["pair","--output",profile],root,input=text+"\n"))
        assert result["imported"] and profile.stat().st_mode&0o777==0o600
        assert "SITEPAIR1." not in json.dumps(result) and secret not in json.dumps(result)
        doctor=json.loads(run(BIN/"site-cli",["--profile",profile,"--transport","relay","doctor"],root))
        assert doctor["authenticated"]
        checks.append("paste pairing text to import a private reusable local profile")
        b,agent_b,env_b=device("two")
        log=(b/"run.log").open("w");handles.append(log)
        second=subprocess.Popen([str(agent_b)],cwd=root,env=env_b,stdout=log,stderr=log);processes.append(second)
        state_b=b/"private/SiteDeck/Agent/agent.json"
        deadline=time.monotonic()+8
        while not state_b.exists():
            assert time.monotonic()<deadline and second.poll() is None;time.sleep(.05)
        one=json.loads(state_a.read_text());two=json.loads(state_b.read_text())
        assert one["deviceId"]!=two["deviceId"] and one["guestToken"]!=two["guestToken"]
        checks.append("the same Hub configuration does not clone identity or guest credential")

        c,agent_c,env_c=device("wizard",configured=False)
        master,slave=pty.openpty()
        wizard=subprocess.Popen([str(agent_c)],cwd=root,env=env_c,stdin=slave,stdout=slave,stderr=slave);processes.append(wizard);os.close(slave)
        console=b""
        def until(marker):
            global console
            deadline=time.monotonic()+10
            while marker not in console:
                assert time.monotonic()<deadline and wizard.poll() is None,"wizard did not reach prompt"
                ready,_,_=select.select([master],[],[],.1)
                if ready:console+=os.read(master,65536)
        until(b"Hub WebSocket")
        os.write(master,(endpoint+"\n").encode())
        until("不回显".encode())
        os.write(master,(secret+"\n").encode())
        until("开启后可远程操作".encode())
        os.write(master,b"host\n")
        until(b"SITEPAIR1.")
        saved=json.loads((c/"site-agent.json").read_text())
        assert saved["hub"]==endpoint and saved["registrationToken"]==secret and saved["permissions"]==["host"]
        assert secret.encode() not in console
        wizard.terminate();wizard.wait(timeout=8);os.close(master)
        checks.append("missing config prompts once, hides registration secret, saves and starts")
        checks.append("manual terminal startup displays pairing text without exporting a file")
        process.terminate();process.wait(timeout=8)
        assert "SITEPAIR1." not in (a/"run.log").read_text()
        checks.append("background service logs do not receive pairing secrets by default")
    finally:
        for process in reversed(processes):
            if process.poll() is None:
                process.terminate()
                try:process.wait(timeout=8)
                except subprocess.TimeoutExpired:process.kill();process.wait()
        for handle in handles:handle.close()

result={"passed":len(checks),"checks":checks}
(BIN/"easy-smoke-result.json").write_text(json.dumps(result,indent=2)+"\n")
print(json.dumps(result,indent=2))
