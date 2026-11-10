"""只下载解析后的媒体；协议下载由 yt-dlp 完成。

输入仅从 stdin 读取，stdout 仅输出固定结构的进度/结果事件。原始异常、
签名 URL、Cookie 和第三方日志不转发到 OpenList 日志或任务错误中。
"""
import json
from pathlib import Path
import sys
import time

class SilentLogger:
    def debug(self, message): pass
    def info(self, message): pass
    def warning(self, message): pass
    def error(self, message): pass

def emit(value):
    print(json.dumps(value), flush=True)

def download(payload):
    import yt_dlp
    last = [0.0]
    def progress(event):
        now = time.monotonic()
        if now-last[0] < .5 and event.get('status') != 'finished': return
        last[0] = now
        emit({'bytes':int(event.get('downloaded_bytes') or 0),
              'total':int(event.get('total_bytes') or event.get('total_bytes_estimate') or 0)})
    options = {
        'quiet':True, 'no_warnings':True, 'noprogress':True, 'logger':SilentLogger(),
        'noplaylist':True, 'format':'bv*+ba/b', 'concurrent_fragment_downloads':4,
        'retries':3, 'fragment_retries':3, 'skip_unavailable_fragments':False,
        'socket_timeout':30, 'continuedl':False, 'overwrites':True,
        # 中间产物仅放在私有工作目录，完成后由 Go 校验并发布。
        'outtmpl':str(Path(payload['work'])/'media.%(ext)s'),
        'ffmpeg_location':payload['ffmpeg'], 'merge_output_format':'mp4',
        'postprocessors':[{'key':'FFmpegVideoRemuxer','preferedformat':'mp4'}],
        'progress_hooks':[progress], 'proxy':'',
        # 只接受回环中转地址，真实源请求由 Go 进行域名/凭据边界检查。
        'allowed_extractors':['generic'],
    }
    with yt_dlp.YoutubeDL(options) as downloader:
        info = downloader.extract_info(payload['url'], download=True)
        if not info or info.get('is_live'): raise ValueError('not a completed video')
    result = Path(payload['work'])/'media.mp4'
    if not result.is_file() or result.stat().st_size <= 0: raise ValueError('missing media')
    emit({'done':True})

if __name__ == '__main__':
    try:
        raw = sys.stdin.buffer.read(65537)
        if len(raw)>65536: raise ValueError('input too large')
        download(json.loads(raw))
    except Exception:
        emit({'error':'DOWNLOAD_FAILED'})
        sys.exit(1)
