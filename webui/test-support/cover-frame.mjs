// Observe the first rendering opportunity after the new song card is committed.
// Await the captured snapshot, never poll until the image eventually appears.
export async function coverOnFirstFrame(page,id,action) {
  await page.evaluate(id=>{
    const frame=new Promise(resolve=>{
      const observer=new MutationObserver(()=>{
        const card=document.querySelector(`[role="group"][aria-label="歌曲 ${id}"]`);
        if(!card)return;
        observer.disconnect();
        requestAnimationFrame(()=>resolve([...card.querySelectorAll('img')].map(image=>({source:image.getAttribute('src'),complete:image.complete,width:image.naturalWidth,visibility:getComputedStyle(image).visibility}))));
      });
      observer.observe(document.body,{childList:true,subtree:true});
    });
    Object.assign(window,{coverFirstFrame:frame});
  },id);
  await action();
  return page.evaluate(()=>window.coverFirstFrame);
}
