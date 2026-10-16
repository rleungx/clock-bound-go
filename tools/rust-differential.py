#!/usr/bin/env python3
"""Generate reproducible same-input results by calling the real upstream Rust projector.

Usage: python3 tools/rust-differential.py --upstream /path/to/aws/clock-bound
Only a helper is appended to an isolated copy; the projection body is untouched.
The original source SHA-256 and git revision are checked before building offline.
For client traces only the hardware-counter read is replaced by a fixed input.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import random
import shutil
import struct
import subprocess
import tempfile

PIN = "526b8a89d2a8b8ac461a3ab61cadc8c93d48a479"
SHM_SHA = "a772deae0a8dda55a60e06b2b64131f1f37c2f112c429f6ef40e994f3a38bfe8"
ROOT = Path(__file__).resolve().parents[1]

HELPER = r'''
// Appended only in an isolated test copy. Original projection code is unchanged.
pub static __GO_DIFF_COUNTER: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
pub fn __go_diff_set_counter(counter: u64) {
    __GO_DIFF_COUNTER.store(counter, std::sync::atomic::Ordering::Relaxed);
}
pub fn __go_diff_decode(b: &[u8]) -> ClockErrorBoundV3 {
    let u = |o: usize| u64::from_le_bytes(b[o..o+8].try_into().unwrap());
    let status = match u32::from_le_bytes(b[76..80].try_into().unwrap()) {
        0 => ClockStatus::Unknown, 1 => ClockStatus::Synchronized,
        2 => ClockStatus::FreeRunning, 3 => ClockStatus::Disrupted,
        _ => panic!("invalid status in test corpus"),
    };
    ClockErrorBoundV3 {
        as_of_tsc: u(0),
        as_of: TimeSpec::new(u(8) as i64, u(16) as i64),
        void_after: TimeSpec::new(u(24) as i64, u(32) as i64),
        period_frac: u(40), period_err_frac: u(48), bound_nsec: u(56) as i64,
        disruption_marker: u(64), max_drift_ppb: u32::from_le_bytes(b[72..76].try_into().unwrap()),
        clock_status: status, clock_disruption_support_enabled: b[80] != 0,
        period_shift: b[81], period_err_shift: b[82], _padding: [0; 5],
    }
}
pub fn __go_diff_project(s: &ClockErrorBoundV3, tsc: u64) -> Result<ClockBoundNowResult, ShmError> {
    s.compute_bound_at_tsc(tsc)
}
'''
DRIVER = r'''
use std::io::{self, BufRead, Write};
use clock_bound::shm::{__go_diff_decode, __go_diff_project, __go_diff_set_counter};
use clock_bound::client::ClockBoundClient;
fn unhex(s: &str) -> Vec<u8> {
    (0..s.len()).step_by(2).map(|i| u8::from_str_radix(&s[i..i+2],16).unwrap()).collect()
}
fn trace(directory: &str) {
    let cb_path = std::path::Path::new(directory).join("clockbound.bin");
    let vm_path = std::path::Path::new(directory).join("vmclock.bin");
    let mut client: Option<ClockBoundClient> = None;
    for line in io::stdin().lock().lines() {
        let line = line.unwrap();
        let fields: Vec<&str> = line.split_whitespace().collect();
        let cb = unhex(fields[0]);
        let vm = unhex(fields[1]);
        let counter: u64 = fields[2].parse().unwrap();
        for (path, bytes) in [(&cb_path, cb), (&vm_path, vm)] {
            let mut file = std::fs::OpenOptions::new().create(true).write(true).truncate(false).open(path).unwrap();
            file.write_all(&bytes).unwrap();
        }
        __go_diff_set_counter(counter);
        if client.is_none() {
            match ClockBoundClient::new_with_paths(cb_path.to_str().unwrap(), vm_path.to_str().unwrap()) {
                Ok(c) => client = Some(c),
                Err(e) => {println!("error {:?}", e.kind);continue;},
            }
        }
        match client.as_mut().unwrap().now() {
            Ok(r) => println!("ok {} {} {} {} {}", r.earliest.tv_sec(),r.earliest.tv_nsec(),r.latest.tv_sec(),r.latest.tv_nsec(),r.clock_status as i32),
            Err(e) => println!("error {:?}", e.kind),
        }
    }
}
fn main() {
    if std::env::args().nth(1).as_deref() == Some("--trace") {
        trace(&std::env::args().nth(2).unwrap());return;
    }
    if std::env::args().nth(1).as_deref() == Some("--bench") {
        let bytes = std::fs::read(std::env::args().nth(2).unwrap()).unwrap();
        let snapshot = __go_diff_decode(&bytes[16..]);
        let iterations = 20_000_000u64;
        for _sample in 0..3 {
            let start = std::time::Instant::now();
            for i in 0..iterations {
                let value = __go_diff_project(std::hint::black_box(&snapshot), std::hint::black_box(2024+(i&15))).unwrap();
                std::hint::black_box(value);
            }
            println!("rust_projection_ns_per_op {:.3}", start.elapsed().as_nanos() as f64 / iterations as f64);
        }
        return;
    }
    for line in io::stdin().lock().lines() {
        let line = line.unwrap();
        let mut parts = line.split_whitespace();
        let bytes = unhex(parts.next().unwrap());
        let tsc: u64 = parts.next().unwrap().parse().unwrap();
        let snapshot = __go_diff_decode(&bytes[16..]);
        match __go_diff_project(&snapshot, tsc) {
            Ok(r) => println!("ok {} {} {} {} {}", r.earliest.tv_sec(),r.earliest.tv_nsec(),r.latest.tv_sec(),r.latest.tv_nsec(),r.clock_status as i32),
            Err(e) => println!("error {:?}",e),
        }
    }
}
'''

def snapshot(asof=1_700_000_000, ns=123_456_789, expiry=120, counter=1000,
             frac=(1<<64)-1, shift=30, err_frac=0, err_shift=40,
             bound=37, drift=100, status=1):
    b=bytearray(104)
    struct.pack_into('<IIIHH',b,0,0x414d5a4e,0x43420200,104,0x0303,2)
    end_ns=ns+expiry
    end_sec=asof+end_ns//1_000_000_000
    struct.pack_into('<QqqqqQQqQII',b,16,counter,asof,ns,end_sec,end_ns%1_000_000_000,
                     frac,err_frac,bound,42,drift,status)
    b[97]=shift;b[98]=err_shift
    return bytes(b)

def cases():
    rng=random.Random(89341)
    rows=[]
    def add(name,b,c,policy='comparable'):
        rows.append(dict(name=name,snapshot=b.hex(),counter=c,policy=policy))
    for i in range(768):
        counter=rng.randrange(1<<40,1<<60)
        b=snapshot(counter=counter,asof=rng.randrange(1_600_000_000,2_100_000_000),
                   ns=rng.randrange(1_000_000_000),expiry=120_000_000_000,
                   frac=rng.randrange(1<<63,1<<64),shift=rng.choice([24,25,29,30,31,32,40]),
                   err_frac=rng.randrange(1<<64),err_shift=rng.randrange(20,60),
                   bound=rng.choice([0,1,37,100_000,1<<53,(1<<53)+1,(1<<53)+3]),
                   drift=rng.choice([0,1,100,1000,1_000_000]),status=rng.randrange(4))
        add('random-%03d'%i,b,counter+rng.randrange(1<<45))
    for status in range(4):
        for elapsed in [0,1,119,120,121,239,240,241]:
            add('threshold-%d-%d'%(status,elapsed),snapshot(frac=1<<63,shift=0,expiry=120_000_000_000,status=status),1000+elapsed)
    # Counter periods with tiny fractional nanoseconds at an expiry boundary.
    for shift in [29,30,60,128,255]:
        for expiry in [0,1,60_000_000_000,120_000_000_000]:
            base=(expiry*(1<<(64+shift)))//((1<<63)*1_000_000_000)
            for offset in [-1,0,1]:
                delta=base+offset
                if 0<=delta<(1<<64)-1000:
                    add('fractional-%d-%d-%d'%(shift,expiry,offset),snapshot(frac=1<<63,shift=shift,expiry=expiry),1000+delta)
    add('backwards',snapshot(),999,'counter_backwards')
    add('negative-bound',snapshot(bound=-1),1001,'malformed')
    add('zero-period',snapshot(frac=0),1001,'uninitialized')
    add('drift-too-large',snapshot(drift=1_000_000_000),1001,'malformed')
    return rows

def trace_scenarios():
    def cb(generation=2,enabled=False,bound=37,marker=42,version=0x0303):
        raw=bytearray(snapshot(shift=10,err_frac=(1<<64)-1,err_shift=20,expiry=120_000_000_000,bound=bound))
        struct.pack_into('<HH',raw,12,version,generation)
        struct.pack_into('<Q',raw,80,marker)
        raw[96]=int(enabled)
        return raw.hex()
    def vm(marker=42,version=1):
        raw=bytearray(104)
        struct.pack_into('<IIHBBIQ',raw,0,0x4b4c4356,104,version,1,0,2 if marker == 42 else 4,marker)
        return raw.hex()
    def step(cb_hex,vm_hex=None,policy='aligned',bound=None):
        result=dict(snapshot=cb_hex,vmclock=vm_hex or vm(),counter=2024,policy=policy)
        if bound is not None:result['expected_bound']=bound
        return result
    yield 'updates-and-reset', [step(cb()),step(cb(4,bound=38)),step(cb(5,bound=99)),step(cb(0,version=0)),step(cb(6,bound=39))]
    yield 'sticky-vmclock', [step(cb(enabled=True)),step(cb(4,enabled=True),vm(43)),step(cb(6,enabled=False),vm(43)),step(cb(8,enabled=False,marker=43),vm(43))]
    yield 'enable-and-recover', [step(cb()),step(cb(4,enabled=True)),step(cb(6,enabled=True),vm(43)),step(cb(8,enabled=True,marker=43),vm(43))]
    yield 'generation-reuse', [step(cb()),step(cb(bound=100),policy='fresh_generation_reuse',bound=100)]
    yield 'unsupported-version', [step(cb()),step(cb(4,version=0x0304)),step(cb(6))]
    yield 'vmclock-version', [step(cb(enabled=True)),step(cb(4,enabled=True),vm(version=2)),step(cb(6,enabled=True))]

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--upstream',required=True,type=Path)
    parser.add_argument('--bench',action='store_true')
    args=parser.parse_args()
    upstream=args.upstream.resolve()
    revision=subprocess.check_output(['git','-C',str(upstream),'rev-parse','HEAD'],text=True).strip()
    subprocess.run(['git','-C',str(upstream),'diff','--quiet','HEAD','--'],check=True)
    original=(upstream/'clock-bound/src/shm.rs').read_bytes()
    if revision!=PIN or hashlib.sha256(original).hexdigest()!=SHM_SHA:
        raise SystemExit('upstream revision or original projection source hash mismatch')
    with tempfile.TemporaryDirectory(prefix='clock-bound-go-diff-') as temp:
        temp=Path(temp)
        copied=temp/'upstream'
        shutil.copytree(upstream,copied,ignore=shutil.ignore_patterns('.git','target'))
        counter_read = b"let now_tsc = read_timestamp_counter_begin();"
        if original.count(counter_read) != 1: raise SystemExit("unexpected counter-read instrumentation point")
        instrumented = original.replace(counter_read, b"let now_tsc = __GO_DIFF_COUNTER.load(std::sync::atomic::Ordering::Relaxed);")
        (copied/'clock-bound/src/shm.rs').write_bytes(instrumented+HELPER.encode())
        runner=temp/'runner';(runner/'src').mkdir(parents=True)
        (runner/'src/main.rs').write_text(DRIVER)
        (runner/'Cargo.toml').write_text('[package]\nname="clock-bound-go-diff"\nversion="0.1.0"\nedition="2024"\n[dependencies]\nclock-bound={path='+json.dumps(str(copied/'clock-bound'))+',default-features=false,features=["client"]}\n')
        environment=dict(os.environ,CARGO_TARGET_DIR=str(temp/'target'))
        subprocess.run(['cargo','build','--offline','--release','--manifest-path',str(runner/'Cargo.toml')],env=environment,check=True)
        executable=temp/'target/release/clock-bound-go-diff'
        rows=cases()
        inputs=''.join(r['snapshot']+' '+str(r['counter'])+'\n' for r in rows)
        outputs=subprocess.check_output([str(executable)],input=inputs,text=True).splitlines()
        if len(rows)!=len(outputs):raise SystemExit('Rust result count mismatch')
        for row,line in zip(rows,outputs):
            words=line.split()
            if words[0]=='ok':
                row['rust']={k:int(v) for k,v in zip(['earliest_sec','earliest_ns','latest_sec','latest_ns','status'],words[1:])}
            else:row['rust_error']=line[6:]
        output=ROOT/'testdata/rust-differential.jsonl'
        output.write_text(''.join(json.dumps(r,separators=(',',':'))+'\n' for r in rows))
        print('wrote',len(rows),'actual Rust projection results to',output)
        traces = []
        for name,steps in trace_scenarios():
            directory=temp/name;directory.mkdir()
            stream=''.join(step['snapshot']+' '+step['vmclock']+' '+str(step['counter'])+'\n' for step in steps)
            responses=subprocess.check_output([str(executable),'--trace',str(directory)],input=stream,text=True).splitlines()
            if len(responses)!=len(steps):raise SystemExit('Rust client trace count mismatch')
            for step,response in zip(steps,responses):
                fields=response.split()
                if fields[0]=='ok':step['rust']={k:int(v) for k,v in zip(['earliest_sec','earliest_ns','latest_sec','latest_ns','status'],fields[1:])}
                else:step['rust_error']=response[6:]
            traces.append(dict(name=name,steps=steps))
        (ROOT/'testdata/rust-reader-traces.json').write_text(json.dumps(traces,separators=(',',':'))+'\n')
        print('wrote',sum(len(t['steps']) for t in traces),'actual Rust client trace steps')
        if args.bench:
            subprocess.run([str(executable),'--bench',str(ROOT/'testdata/upstream-v3.bin')],check=True)
if __name__=='__main__':main()
