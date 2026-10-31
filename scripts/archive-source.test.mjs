import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {extractDashboardArchive} from './dashboard-source.mjs';
import {withTemp,run,createArchive,extractArchive,filesUnder,sha256} from './release-source.mjs';

const here=path.dirname(fileURLToPath(import.meta.url));

test('dashboard ZIP preserves Unicode filenames through source delivery', {skip:process.platform!=='win32'},()=>withTemp(temp=>{
  const source=path.join(temp,'source'),archive=path.join(temp,"upstream's source.zip"),unpack=path.join(temp,'dashboard unpack');
  const entries={'ascii.txt':'plain content','.github/ISSUE_TEMPLATE/缺陷-😀.yml':'UTF-8 source content'};
  for(const [name,content] of Object.entries(entries)){
    const file=path.join(source,name);fs.mkdirSync(path.dirname(file),{recursive:true});fs.writeFileSync(file,content);
  }
  run('pwsh',['-NoProfile','-File',path.join(here,'archive-portable.ps1'),'-Folder',source,'-Zip',archive]);
  const originalHash=sha256(archive);
  fs.mkdirSync(unpack);extractDashboardArchive(archive,unpack);
  const check=dir=>{
    assert.deepEqual(filesUnder(path.join(dir,'source')),Object.keys(entries).sort());
    for(const [name,content] of Object.entries(entries))assert.equal(fs.readFileSync(path.join(dir,'source',name),'utf8'),content);
  };
  check(unpack);assert.equal(sha256(archive),originalHash);
  const delivered=path.join(temp,'source.tar.gz'),roundtrip=path.join(temp,'roundtrip');
  createArchive(path.join(unpack,'source'),delivered);
  extractArchive(delivered,roundtrip,'source');check(roundtrip);
}));

test('Windows source extraction rejects unsafe names and links before writing', {skip:process.platform!=='win32'},()=>withTemp(temp=>{
  const writer=path.join(temp,'write-tar.ps1');
  fs.writeFileSync(writer,`param($SpecFile,$ArchiveFile)
$ErrorActionPreference='Stop'
$spec=Get-Content -LiteralPath $SpecFile -Raw -Encoding UTF8 | ConvertFrom-Json
$file=[IO.File]::Create($ArchiveFile)
try {
  $writer=[System.Formats.Tar.TarWriter]::new($file,$true)
  try {
    foreach($item in $spec) {
      $entry=[System.Formats.Tar.UstarTarEntry]::new([System.Formats.Tar.TarEntryType]$item.type,$item.name)
      if($item.link) {$entry.LinkName=$item.link}
      $writer.WriteEntry($entry)
    }
  } finally {$writer.Dispose()}
} finally {$file.Dispose()}
`);
  const regular=name=>({name,type:'RegularFile'});
  const cases=[
    [regular('source/../outside')], [regular('/absolute')], [regular('source/a\\b')],
    [regular('source/a\nsource/b')], [regular('source/a:b')], [regular('other/file')],
    [regular('source/file'),regular('source/file')],
    [{name:'source/link',type:'SymbolicLink',link:'../outside'}],
    [{name:'source/link',type:'HardLink',link:'../outside'}],
  ];
  for(const [i,entries] of cases.entries()){
    const spec=path.join(temp,`spec-${i}.json`),archive=path.join(temp,`bad-${i}.tar`),dest=path.join(temp,`output-${i}`);
    fs.writeFileSync(spec,JSON.stringify(entries));
    run('pwsh',['-NoProfile','-File',writer,spec,archive]);
    assert.throws(()=>extractArchive(archive,dest,'source'),/Unsafe path|Unexpected archive entry|links or special entries/);
    assert.equal(fs.existsSync(dest),false);
  }
}));
