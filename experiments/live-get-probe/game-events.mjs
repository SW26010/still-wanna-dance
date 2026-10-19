import fs from 'node:fs';
const lines=fs.readFileSync(process.argv[2],'utf8').split(/\r?\n/);
const events=[];
for(const [i,line] of lines.entries()){
 const time=line.match(/^(\d{4})\.(\d{2})\.(\d{2}) (\d{2}:\d{2}:\d{2})/);
 if(!time)continue;
 const date=`${time[1]}-${time[2]}-${time[3]}T${time[4]}+08:00`;
 let event;
 if(/Video loaded \(/.test(line))event='video-loaded';
 else if(/OnVideoStart: Started video/.test(line))event='video-start';
 else if(/OnVideoError/.test(line))event='video-error';
 else if(/\[VRCX\] VideoPlay/.test(line))event='formal-request';
 else if(/Attempting to resolve/.test(line))event='resolver-request';
 else if(/DelayedVideoReady: Time/.test(line))event='delayed-ready';
 if(!event)continue;
 events.push({time:date,event,line:i+1,song:line.match(/[?&]id=(\d+)/)?.[1],loadSeconds:Number(line.match(/Video loaded \(([\d.]+)/)?.[1])||undefined,waitSeconds:Number(line.match(/wait for ([\d.]+)/)?.[1])||undefined});
}
console.log(JSON.stringify(events.slice(-30),null,2));
