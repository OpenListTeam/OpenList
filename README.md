<div align="center">
  <img style="width: 128px; height: 128px;" src="https://raw.githubusercontent.com/OpenListTeam/Logo/main/logo.svg" alt="logo" />

  <p><em>OpenList is a resilient, long-term governance, community-driven fork of AList — built to defend open source against trust-based attacks.</em></p>

  <img src="https://goreportcard.com/badge/github.com/OpenListTeam/OpenList/v3" alt="latest version" />
  <a href="https://github.com/OpenListTeam/OpenList/blob/main/LICENSE"><img src="https://img.shields.io/github/license/OpenListTeam/OpenList" alt="License" /></a>
  <a href="https://github.com/OpenListTeam/OpenList/actions?query=workflow%3ABuild"><img src="https://img.shields.io/github/actions/workflow/status/OpenListTeam/OpenList/build.yml?branch=main" alt="Build status" /></a>
  <a href="https://github.com/OpenListTeam/OpenList/releases"><img src="https://img.shields.io/github/release/OpenListTeam/OpenList" alt="latest version" /></a>

  <a href="https://github.com/OpenListTeam/OpenList/discussions"><img src="https://img.shields.io/github/discussions/OpenListTeam/OpenList?color=%23ED8936" alt="discussions" /></a>
  <a href="https://github.com/OpenListTeam/OpenList/releases"><img src="https://img.shields.io/github/downloads/OpenListTeam/OpenList/total?color=%239F7AEA&logo=github" alt="Downloads" /></a>
</div>

---

- English | [中文](./README_cn.md) | [日本語](./README_ja.md) | [Dutch](./README_nl.md)

- [Contributing](./CONTRIBUTING.md)
- [CODE OF CONDUCT](./CODE_OF_CONDUCT.md)
- [LICENSE](./LICENSE)

## Disclaimer

OpenList is an open-source project independently maintained by the OpenList Team, following the AGPL-3.0 license and committed to maintaining complete code openness and modification transparency.

We have noticed the emergence of some third-party projects in the community with names similar to this project, such as OpenListApp/OpenListApp, as well as some paid proprietary software using the same or similar naming. To avoid user confusion, we hereby declare:

- OpenList has no official association with any third-party derivative projects.

- All software, code, and services of this project are maintained by the OpenList Team and are freely available on GitHub.

- Project documentation and API services primarily rely on charitable resources provided by Cloudflare. There are currently no paid plans or commercial deployments, and the use of existing features does not involve any costs.

We respect the community's rights to free use and derivative development, but we also strongly urge downstream projects:

- Should not use the "OpenList" name for impersonation promotion or commercial gain;

- Must not distribute OpenList-based code in a closed-source manner or violate AGPL license terms.

To better maintain healthy ecosystem development, we recommend:

- Clearly indicate the project source and choose appropriate open-source licenses in accordance with the open-source spirit;

- If involving commercial use, please avoid using "OpenList" or any confusing naming as the project name;

- If you need to use materials located under OpenListTeam/Logo, you may modify and use them under compliance with the agreement.

Thank you for your support and understanding of the OpenList project.

## Features

- [x] Multiple storages
  - [x] Local storage
  - [x] [Aliyundrive](https://www.alipan.com)
  - [x] OneDrive / Sharepoint ([Global](https://www.microsoft.com/en-us/microsoft-365/onedrive/online-cloud-storage), [CN](https://portal.partner.microsoftonline.cn), DE, US)
  - [x] [189cloud](https://cloud.189.cn) (Personal, Family)
  - [x] [GoogleDrive](https://drive.google.com)
  - [x] [123pan](https://www.123pan.com)
  - [x] [FTP / SFTP](https://en.wikipedia.org/wiki/File_Transfer_Protocol)
  - [x] [PikPak](https://www.mypikpak.com)
  - [x] [S3](https://aws.amazon.com/s3)
  - [x] [Seafile](https://seafile.com)
  - [x] [UPYUN Storage Service](https://www.upyun.com/products/file-storage)
  - [x] [WebDAV](https://en.wikipedia.org/wiki/WebDAV)
  - [x] Teambition([China](https://www.teambition.com), [International](https://us.teambition.com))
  - [x] [MediaFire](https://www.mediafire.com)
  - [x] [Mediatrack](https://www.mediatrack.cn)
  - [x] [ProtonDrive](https://proton.me/drive)
  - [x] [139yun](https://yun.139.com) (Personal, Family, Group)
  - [x] [YandexDisk](https://disk.yandex.com)
  - [x] [BaiduNetdisk](http://pan.baidu.com)
  - [x] [Terabox](https://www.terabox.com/main)
  - [x] [UC](https://drive.uc.cn)
  - [x] [Quark](https://pan.quark.cn)
  - [x] [Thunder](https://pan.xunlei.com)
  - [x] [Lanzou](https://www.lanzou.com)
  - [x] [ILanzou](https://www.ilanzou.com)
  - [x] [Google photo](https://photos.google.com)
  - [x] [Mega.nz](https://mega.nz)
  - [x] [Baidu photo](https://photo.baidu.com)
  - [x] [SMB](https://en.wikipedia.org/wiki/Server_Message_Block)
  - [x] [115](https://115.com)
  - [X] [Cloudreve](https://cloudreve.org)
  - [x] [Dropbox](https://www.dropbox.com)
  - [x] [FeijiPan](https://www.feijipan.com)
  - [x] [dogecloud](https://www.dogecloud.com/product/oss)
  - [x] [Azure Blob Storage](https://azure.microsoft.com/products/storage/blobs)
  - [x] [Chaoxing](https://www.chaoxing.com)
  - [x] [CNB](https://cnb.cool/)
  - [x] [Degoo](https://degoo.com)
  - [x] [Doubao](https://www.doubao.com)
  - [x] [Febbox](https://www.febbox.com)
  - [x] [GitHub](https://github.com)
  - [x] [OpenList](https://github.com/OpenListTeam/OpenList)
  - [x] [Teldrive](https://github.com/tgdrive/teldrive)
  - [x] [Weiyun](https://www.weiyun.com)
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
- [x] WebDAV
- [x] Docker Deploy
- [x] Cloudflare Workers proxy
- [x] File/Folder package download
- [x] Web upload(Can allow visitors to upload), delete, mkdir, rename, move and copy
- [x] Offline download
- [x] Copy files between two storage
- [x] Multi-thread downloading acceleration for single-thread download/stream

## Document

- 📘 [Global Site](https://doc.oplist.org)
- 📚 [Backup Site](https://doc.openlist.team)
- 🌏 [CN Site](https://doc.oplist.org.cn)

## Demo

- 🌎 [Global Demo](https://demo.oplist.org)
- 🇨🇳 [CN Demo](https://demo.oplist.org.cn)

## Discussion

Please refer to [*Discussions*](https://github.com/OpenListTeam/OpenList/discussions) for raising general questions, ***Issues* is for bug reports and feature requests only.**

## Sponsor

[![VPS.Town](https://vps.town/static/images/sponsor.png)](https://vps.town "VPS.Town - Trust, Effortlessly. Your Cloud, Reimagined.")

## License

The `OpenList` is open-source software licensed under the [AGPL-3.0](https://www.gnu.org/licenses/agpl-3.0.txt) license.

## Disclaimer

- This project is a free and open-source software designed to facilitate file sharing via net disks, primarily intended to support the downloading and learning of the Go programming language.
- Please comply with all applicable laws and regulations when using this software. Any form of misuse is strictly prohibited.
- The software is based on official SDKs or APIs without any modification, disruption, or interference with their behavior.
- It only performs HTTP 302 redirects or traffic forwarding, and does not intercept, store, or tamper with any user data.
- This project is not affiliated with any official platform or service provider.
- The software is provided "as is", without any warranties of any kind, either express or implied, including but not limited to warranties of merchantability or fitness for a particular purpose.
- The maintainers are not liable for any direct or indirect damages arising from the use of, or inability to use, this software.
- You are solely responsible for any risks associated with using this software, including but not limited to account bans or download speed limitations.
- This project is licensed under the [AGPL-3.0](https://www.gnu.org/licenses/agpl-3.0.txt) License. Please see the [LICENSE](./LICENSE) file for details.

## Contact Us

- [@GitHub](https://github.com/OpenListTeam)
- [Telegram Group](https://t.me/OpenListTeam)
- [Telegram Channel](https://t.me/OpenListOfficial)

## Contributors

We sincerely thank the author [Xhofe](https://github.com/Xhofe) of the original project [AlistGo/alist](https://github.com/AlistGo/alist) and all other contributors.

Thanks goes to these wonderful people:

[![Contributors](https://contrib.rocks/image?repo=OpenListTeam/OpenList)](https://github.com/OpenListTeam/OpenList/graphs/contributors)
