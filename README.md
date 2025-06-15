<div align="center">
  <img width="100px" alt="logo" src="https://raw.githubusercontent.com/OpenListTeam/Logo/main/OpenList.svg"/></a>
  <p><em>🗂️A file list program that supports multiple storages, powered by Gin and SolidJS, fork of AList.</em></p>
<div>
  <a href="https://goreportcard.com/report/github.com/OpenListTeam/OpenList/v3">
    <img src="https://goreportcard.com/badge/github.com/OpenListTeam/OpenList/v3" alt="latest version" />
  </a>
  <a href="https://github.com/OpenListTeam/OpenList/blob/main/LICENSE">
    <img src="https://img.shields.io/github/license/OpenListTeam/OpenList" alt="License" />
  </a>
  <a href="https://github.com/OpenListTeam/OpenList/actions?query=workflow%3ABuild">
    <img src="https://img.shields.io/github/actions/workflow/status/OpenListTeam/OpenList/build.yml?branch=main" alt="Build status" />
  </a>
  <a href="https://github.com/OpenListTeam/OpenList/releases">
    <img src="https://img.shields.io/github/release/OpenListTeam/OpenList" alt="latest version" />
  </a>
</div>
<div>
  <a href="https://github.com/OpenListTeam/OpenList/discussions">
    <img src="https://img.shields.io/github/discussions/OpenListTeam/OpenList?color=%23ED8936" alt="discussions" />
  </a>
  <a href="https://github.com/OpenListTeam/OpenList/releases">
    <img src="https://img.shields.io/github/downloads/OpenListTeam/OpenList/total?color=%239F7AEA&logo=github" alt="Downloads" />
  </a>
</div>
</div>

---

> [!IMPORTANT]
>
> Drop-in replacement for AList with long-term governance, no hidden risks, and full transparency, built to defend open source against trust-based attacks.
>
> We sincerely thank the author [Xhofe](https://github.com/Xhofe) of the original project [AlistGo/alist](https://github.com/AlistGo/alist) and all other contributors.
>
> This fork is not yet stable, specific migration progress can be viewed in [OpenList Migration Work Summary](https://github.com/OpenListTeam/OpenList/issues/6).

English | [中文](./README_cn.md) | [日本語](./README_ja.md) | [Contributing](./CONTRIBUTING.md) | [CODE OF CONDUCT](./CODE_OF_CONDUCT.md)

## Features

- [x] Multiple storages
    - [x] Local storage
    - [x] [Aliyundrive](https://www.alipan.com/)
    - [x] OneDrive / Sharepoint ([global](https://www.office.com/), [cn](https://portal.partner.microsoftonline.cn),de,us)
    - [x] [189cloud](https://cloud.189.cn) (Personal, Family)
    - [x] [GoogleDrive](https://drive.google.com/)
    - [x] [123pan](https://www.123pan.com/)
    - [x] FTP / SFTP
    - [x] [PikPak](https://www.mypikpak.com/)
    - [x] [S3](https://aws.amazon.com/s3/)
    - [x] [Seafile](https://seafile.com/)
    - [x] [UPYUN Storage Service](https://www.upyun.com/products/file-storage)
    - [x] WebDav(Support OneDrive/SharePoint without API)
    - [x] Teambition([China](https://www.teambition.com/ ),[International](https://us.teambition.com/ ))
    - [x] [Mediatrack](https://www.mediatrack.cn/)
    - [x] [139yun](https://yun.139.com/) (Personal, Family, Group)
    - [x] [YandexDisk](https://disk.yandex.com/)
    - [x] [BaiduNetdisk](http://pan.baidu.com/)
    - [x] [Terabox](https://www.terabox.com/main)
    - [x] [UC](https://drive.uc.cn)
    - [x] [Quark](https://pan.quark.cn)
    - [x] [Thunder](https://pan.xunlei.com)
    - [x] [Lanzou](https://www.lanzou.com/)
    - [x] [ILanzou](https://www.ilanzou.com/)
    - [x] [Aliyundrive share](https://www.alipan.com/)
    - [x] [Google photo](https://photos.google.com/)
    - [x] [Mega.nz](https://mega.nz)
    - [x] [Baidu photo](https://photo.baidu.com/)
    - [x] SMB
    - [x] [115](https://115.com/)
    - [X] Cloudreve
    - [x] [Dropbox](https://www.dropbox.com/)
    - [x] [FeijiPan](https://www.feijipan.com/)
    - [x] [dogecloud](https://www.dogecloud.com/product/oss)
    - [x] [Azure Blob Storage](https://azure.microsoft.com/products/storage/blobs)
- [x] Easy to deploy and out-of-the-box
- [x] File preview (PDF, markdown, code, plain text, ...)
- [x] Image preview in gallery mode
- [x] Video and audio preview, support lyrics and subtitles
- [x] Office documents preview (docx, pptx, xlsx, ...)
- [x] `README.md` preview rendering
- [x] File permalink copy and direct file download
- [x] Dark mode
- [x] I18n
- [x] Protected routes (password protection and authentication)
- [x] WebDav (waiting for detail documents)
- [ ] Docker Deploy (rebuilding)
- [x] Cloudflare Workers proxy
- [x] File/Folder package download
- [x] Web upload(Can allow visitors to upload), delete, mkdir, rename, move and copy
- [x] Offline download
- [x] Copy files between two storage
- [x] Multi-thread downloading acceleration for single-thread download/stream

## Document

<https://docs.openlist.team>

## Demo

N/A (to be rebuilt)

## Discussion

Please refer to [*Discussions*](https://github.com/OpenListTeam/OpenList/discussions) for raising general questions, ***Issues* is for bug reports and feature requests only.**

## Contributors

Thanks goes to these wonderful people:

[![Contributors](https://contrib.rocks/image?repo=OpenListTeam/OpenList)](https://github.com/OpenListTeam/OpenList/graphs/contributors)

## License

The `OpenList` is open-source software licensed under the AGPL-3.0 license.

## Disclaimer

- This program is a free and open source project. It is designed to share files on the network disk, which is convenient for downloading and learning Golang. Please abide by relevant laws and regulations when using it, and do not abuse it;
- This program is implemented by calling the official sdk/interface, without destroying the official interface behavior;
- This program only does 302 redirect/traffic forwarding, and does not intercept, store, or tamper with any user data;
- Before using this program, you should understand and bear the corresponding risks, including but not limited to account ban, download speed limit, etc., which is none of this program's business;
- If there is any infringement, please contact [OpenListTeam](https://github.com/OpenListTeam), and it will be dealt with in time.

---

> [@GitHub](https://github.com/OpenListTeam) · [Telegram Group](https://t.me/OpenListTeam)
