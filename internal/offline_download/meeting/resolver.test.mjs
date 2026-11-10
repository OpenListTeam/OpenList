import test from 'node:test'
import assert from 'node:assert/strict'
import { resolveShare, platformFor, mediaKind } from './resolver.mjs'

test('platform and media segment boundaries',()=>{
 assert.equal(platformFor('https://meeting.tencent.com/cw/test'),'tencent')
 assert.equal(platformFor('https://tenant.feishu.cn/minutes/test'),'feishu')
 assert.throws(()=>platformFor('https://meeting.tencent.com.evil.example/cw/test'))
 assert.equal(mediaKind('https://cdn.example/init.mp4','video/mp4'),'')
 assert.equal(mediaKind('https://cdn.example/seg.m4s','video/mp4'),'')
})

// 两个平台分别验证有/无 Cookie；路由夹具不会访问真实网站或真实账号。
for(const url of ['https://meeting.tencent.com/cw/test','https://tenant.feishu.cn/minutes/test']){
 for(const cookie of ['', 'session=fixture-only']){
  test(`extract ${url} cookie=${Boolean(cookie)}`,{skip:!process.env.MEETING_TEST_BROWSER},async()=>{
   let observed=''
   const result=await resolveShare({url,cookie,browser_path:process.env.MEETING_TEST_BROWSER,module_dir:process.env.MEETING_TEST_MODULES},async context=>{
    await context.route('**/*',async route=>{
     const req=route.request()
     if(req.url()===url)await route.fulfill({contentType:'text/html',body:'<video autoplay muted src="/fixture.mp4"></video>'})
     else if(req.url().endsWith('/fixture.mp4')){
      observed=req.headers().cookie||''
      await route.fulfill({contentType:'video/mp4',body:Buffer.from('media-response')})
     }else await route.abort()
    })
   })
   assert.equal(result.kind,'mp4');assert.equal(result.url,new URL('/fixture.mp4',url).href)
   assert.equal(observed,cookie)
  })
 }
}
