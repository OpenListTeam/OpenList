<div align="center">
  <img style="width: 128px; height: 128px;" src="https://raw.githubusercontent.com/OpenListTeam/Logo/main/logo.svg" alt="logo" />

  <p><em>OpenList は、信頼ベースの攻撃からオープンソースを守るために構築された、レジリエントで長期ガバナンス、コミュニティ主導の AList フォークです。</em></p>

  <img src="https://goreportcard.com/badge/github.com/OpenListTeam/OpenList/v3" alt="latest version" />
  <a href="https://github.com/OpenListTeam/OpenList/blob/main/LICENSE"><img src="https://img.shields.io/github/license/OpenListTeam/OpenList" alt="License" /></a>
  <a href="https://github.com/OpenListTeam/OpenList/actions?query=workflow%3ABuild"><img src="https://img.shields.io/github/actions/workflow/status/OpenListTeam/OpenList/build.yml?branch=main" alt="Build status" /></a>
  <a href="https://github.com/OpenListTeam/OpenList/releases"><img src="https://img.shields.io/github/release/OpenListTeam/OpenList" alt="latest version" /></a>

  <a href="https://github.com/OpenListTeam/OpenList/discussions"><img src="https://img.shields.io/github/discussions/OpenListTeam/OpenList?color=%23ED8936" alt="discussions" /></a>
  <a href="https://github.com/OpenListTeam/OpenList/releases"><img src="https://img.shields.io/github/downloads/OpenListTeam/OpenList/total?color=%239F7AEA&logo=github" alt="Downloads" /></a>
</div>

---

- [English](./README.md) | [中文](./README_cn.md) | 日本語 | [Dutch](./README_nl.md)

- [コントリビュート](./CONTRIBUTING.md)
- [行動規範](./CODE_OF_CONDUCT.md)
- [ライセンス](./LICENSE)

## 免責事項

OpenListは、OpenListチームが独立して維持するオープンソースプロジェクトであり、AGPL-3.0ライセンスに従い、完全なコードの開放性と変更の透明性を維持することに専念しています。

コミュニティ内で、OpenListApp/OpenListAppなど、本プロジェクトと類似した名称を持つサードパーティプロジェクトや、同一または類似した命名を採用する有料専有ソフトウェアが出現していることを確認しています。ユーザーの誤解を避けるため、以下のように宣言いたします：

- OpenListは、いかなるサードパーティ派生プロジェクトとも公式な関連性はありません。

- 本プロジェクトのすべてのソフトウェア、コード、サービスはOpenListチームによって維持され、GitHubで無料で取得できます。

- プロジェクトドキュメントとAPIサービスは主にCloudflareが提供する公益リソースに依存しており、現在有料プランや商業展開はなく、既存機能の使用に費用は発生しません。

私たちはコミュニティの自由な使用と派生開発の権利を尊重しますが、下流プロジェクトに強く呼びかけます：

- 「OpenList」の名前で偽装宣伝や商業利益を得るべきではありません；

- OpenListベースのコードをクローズドソースで配布したり、AGPLライセンス条項に違反してはいけません。

エコシステムの健全な発展をより良く維持するため、以下を推奨します：

- プロジェクトの出典を明確に示し、オープンソース精神に合致する適切なオープンソースライセンスを選択する；

- 商業用途が関わる場合は、「OpenList」や混乱を招く可能性のある名前をプロジェクト名として使用することを避ける；

- OpenListTeam/Logo下の素材を使用する必要がある場合は、協定を遵守した上で修正して使用できます。

OpenListプロジェクトへのご支援とご理解をありがとうございます。

## 特徴

- [x] 複数ストレージ
  - [x] ローカルストレージ
  - [x] [Aliyundrive](https://www.alipan.com)
  - [x] OneDrive / Sharepoint ([グローバル](https://www.microsoft.com/en-us/microsoft-365/onedrive/online-cloud-storage), [中国](https://portal.partner.microsoftonline.cn), DE, US)
  - [x] [189cloud](https://cloud.189.cn)（個人、家族）
  - [x] [GoogleDrive](https://drive.google.com)
  - [x] [123pan](https://www.123pan.com)
  - [x] [FTP / SFTP](https://en.wikipedia.org/wiki/File_Transfer_Protocol)
  - [x] [PikPak](https://www.mypikpak.com)
  - [x] [S3](https://aws.amazon.com/s3)
  - [x] [Seafile](https://seafile.com)
  - [x] [UPYUN Storage Service](https://www.upyun.com/products/file-storage)
  - [x] [WebDAV](https://en.wikipedia.org/wiki/WebDAV)
  - [x] Teambition([中国](https://www.teambition.com), [国際](https://us.teambition.com))
  - [x] [Mediatrack](https://www.mediatrack.cn)
  - [x] [ProtonDrive](https://proton.me/drive)
  - [x] [139yun](https://yun.139.com)（個人、家族、グループ）
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
  - [x] [Cloudreve](https://cloudreve.org)
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
  - [x] [MediaFire](https://www.mediafire.com)
- [x] 簡単にデプロイでき、すぐに使える
- [x] ファイルプレビュー（PDF、markdown、コード、テキストなど）
- [x] ギャラリーモードでの画像プレビュー
- [x] ビデオ・オーディオプレビュー、歌詞・字幕対応
- [x] Officeドキュメントプレビュー（docx、pptx、xlsxなど）
- [x] `README.md` プレビュー表示
- [x] ファイルのパーマリンクコピーと直接ダウンロード
- [x] ダークモード
- [x] 国際化対応
- [x] 保護されたルート（パスワード保護と認証）
- [x] WebDAV
- [x] Dockerデプロイ
- [x] Cloudflare Workersプロキシ
- [x] ファイル/フォルダのパッケージダウンロード
- [x] Webアップロード（訪問者のアップロード許可可）、削除、フォルダ作成、リネーム、移動、コピー
- [x] オフラインダウンロード
- [x] ストレージ間のファイルコピー
- [x] 単一ファイルのマルチスレッドダウンロード/ストリーム加速

## ドキュメント

- 📘 [グローバルサイト](https://doc.oplist.org)
- 📚 [バックアップサイト](https://doc.openlist.team)
- 🌏 [CNサイト](https://doc.oplist.org.cn)

## デモ

- 🌎 [グローバルデモ](https://demo.oplist.org)
- 🇨🇳 [CNデモ](https://demo.oplist.org.cn)

## ディスカッション

一般的な質問は [*Discussions*](https://github.com/OpenListTeam/OpenList/discussions) をご利用ください。***Issues* はバグ報告と機能リクエスト専用です。**

## スポンサー

[![VPS.Town](https://vps.town/static/images/sponsor.png)](https://vps.town "VPS.Town - Trust, Effortlessly. Your Cloud, Reimagined.")

## ライセンス

「OpenList」は [AGPL-3.0](https://www.gnu.org/licenses/agpl-3.0.txt) ライセンスの下で公開されているオープンソースソフトウェアです。

## 免責事項

- 本プロジェクトは無料のオープンソースソフトウェアであり、ネットワークディスクを通じたファイル共有を容易にすることを目的とし、主に Go 言語のダウンロードと学習をサポートします。
- 本ソフトウェアの利用にあたっては、関連する法令を遵守し、不正利用を固く禁じます。
- 本ソフトウェアは公式 SDK または API に基づいており、その動作を一切改変・破壊・妨害しません。
- 302 リダイレクトまたはトラフィック転送のみを行い、ユーザーデータの傍受・保存・改ざんは一切行いません。
- 本プロジェクトは、いかなる公式プラットフォームやサービスプロバイダーとも関係ありません。
- 本ソフトウェアは「現状有姿」で提供されており、商品性や特定目的への適合性を含むいかなる保証もありません。
- 本ソフトウェアの使用または使用不能によるいかなる直接的・間接的損害についても、メンテナは責任を負いません。
- 本ソフトウェアの利用に伴うすべてのリスク（アカウントの凍結やダウンロード速度制限などを含む）は、利用者自身が負うものとします。
- 本プロジェクトは [AGPL-3.0](https://www.gnu.org/licenses/agpl-3.0.txt) ライセンスに従います。詳細は [LICENSE](./LICENSE) ファイルをご覧ください。

## お問い合わせ

- [@GitHub](https://github.com/OpenListTeam)
- [Telegram グループ](https://t.me/OpenListTeam)
- [Telegram チャンネル](https://t.me/OpenListOfficial)

## コントリビューター

オリジナルプロジェクト [AlistGo/alist](https://github.com/AlistGo/alist) の作者 [Xhofe](https://github.com/Xhofe) およびその他すべての貢献者に心より感謝いたします。

素晴らしい皆様に感謝します：

[![Contributors](https://contrib.rocks/image?repo=OpenListTeam/OpenList)](https://github.com/OpenListTeam/OpenList/graphs/contributors)
