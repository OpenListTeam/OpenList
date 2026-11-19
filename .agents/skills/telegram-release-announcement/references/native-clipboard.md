# 原生剪贴板格式

## Telegram macOS 入口

交付时向剪贴板写入以下类型，ChatInputView.processPaste 通过专用类型识别原生 Document：

- `org.telegram.macos.richtexteditor.fragment`：UTF-8 JSON 编码的 Document
- `public.utf8-plain-text`：可读纯文本，对应 AppKit 的 NSPasteboardTypeString
- `public.html`：HTML 预览，对应 AppKit 的 NSPasteboardTypeHTML

专用类型可直接进入富文本编辑器。该入口要求 macOS 12 或以上，并启用客户端的 isRichTextEditorEnabled 配置。普通输入框的纯文本和 RTF 粘贴另走文本处理流程，不能由此推导出 Markdown 会自动进入新富文本编辑器。

通过 JXA 调用 AppKit 时，使用 `ObjC.import('AppKit')` 和 `ObjC.import('Foundation')`。先完整构造所有载荷，再清空 NSPasteboard.generalPasteboard，分别写入三个类型并检查写入结果。Document JSON 先用 NSString.dataUsingEncoding(NSUTF8StringEncoding) 转为 NSData，再使用 setDataForType 写入专用类型，纯文本和 HTML 使用 setStringForType。

## Document 最小结构

```json
{
  "schemaVersion": 1,
  "layoutDirection": "auto",
  "blocks": [
    {
      "type": "paragraph",
      "value": {
        "id": "paragraph-1",
        "style": "heading1",
        "paragraph": {
          "alignment": "natural",
          "firstLineIndent": 0,
          "headIndent": 0,
          "paragraphSpacingBefore": 0,
          "paragraphSpacingAfter": 0,
          "lineHeightMultiple": 1
        },
        "runs": [
          {
            "text": "发布标题",
            "attributes": {}
          }
        ]
      }
    }
  ]
}
```

每个块的 id 在同一文档内唯一。Block 使用 `type` 和 `value` 包装，BlockID 编码为字符串。

段落 style 使用 `body`、`heading1`、`heading2` 或 `heading3`。正文、标题和独立段落分别建块，不把整篇内容塞进一个带换行的 run。

TextRun 的 attributes 支持以下字段，普通文本使用空对象：

```json
{"bold": true}
{"italic": true}
{"inlineCode": true}
{"link": "https://example.org/release"}
```

同一段内不同格式拆成多个 runs。JSON 字符串编码与 HTML 转义分别处理，不将未经转义的文案拼进 HTML。

有序列表在段落 value 内加入：

```json
{"list": {"marker": "ordered", "level": 0}}
```

无序列表 marker 使用 `bullet`，嵌套层级从 0 开始递增。列表显示文本不再带 Markdown 编号或项目符号。编号由列表结构生成，用户要求保留非连续编号时，改用带原编号的普通段落。

## 核对来源

以下是已使用过的官方源码入口。客户端行为变化或载荷解码失败时，先核对对应实现及客户端版本，再调整格式。

- [ChatInputView.processPaste](https://github.com/TelegramMessenger/telegram-macos/blob/24638bc69bd083981b178dd1e7f921eebbf7fc46/packages/ChatUI/Sources/ChatUI/ChatInputView.swift)
- [剪贴板类型声明](https://github.com/TelegramMessenger/telegram-macos/blob/24638bc69bd083981b178dd1e7f921eebbf7fc46/packages/RichTextEditorUI/Sources/RichTextEditorUI/Canvas/DocumentCanvasView%2BEditMenu.swift)
- [Document](https://github.com/TelegramMessenger/Telegram-iOS/blob/38309ec95e59be3958d1e6fe9be331d72527a38d/submodules/TelegramUI/Components/RichTextEditor/Sources/RichTextEditorCore/Model/Document.swift)
- [Block](https://github.com/TelegramMessenger/Telegram-iOS/blob/38309ec95e59be3958d1e6fe9be331d72527a38d/submodules/TelegramUI/Components/RichTextEditor/Sources/RichTextEditorCore/Model/Block.swift)
- [ParagraphBlock](https://github.com/TelegramMessenger/Telegram-iOS/blob/38309ec95e59be3958d1e6fe9be331d72527a38d/submodules/TelegramUI/Components/RichTextEditor/Sources/RichTextEditorCore/Model/ParagraphBlock.swift)
- [CharacterAttributes](https://github.com/TelegramMessenger/Telegram-iOS/blob/38309ec95e59be3958d1e6fe9be331d72527a38d/submodules/TelegramUI/Components/RichTextEditor/Sources/RichTextEditorCore/Model/CharacterAttributes.swift)
- [ListMembership](https://github.com/TelegramMessenger/Telegram-iOS/blob/38309ec95e59be3958d1e6fe9be331d72527a38d/submodules/TelegramUI/Components/RichTextEditor/Sources/RichTextEditorCore/Model/ListMembership.swift)
