package cronjob

// syncHandlerInfo 描述 sync 任务的所有前端配置字段。
// 字段顺序就是前端表单顺序；后续修改字段时只需要同时维护 SyncArgs 和这里。
func syncHandlerInfo() HandlerInfo {
	return HandlerInfo{
		Type:           "sync",
		LabelKey:       "cronjobs.types.sync",
		DescriptionKey: "cronjobs.descriptions.sync",
		Fields: []ArgField{
			{
				Name:     "src",
				Type:     ArgFieldTypePath,
				LabelKey: "cronjobs.args.sync.src",
				HelpKey:  "cronjobs.args.sync.src_help",
				Required: true,
			},
			{
				Name:     "dst",
				Type:     ArgFieldTypePath,
				LabelKey: "cronjobs.args.sync.dst",
				HelpKey:  "cronjobs.args.sync.dst_help",
				Required: true,
			},
			{
				Name:     "max_depth",
				Type:     ArgFieldTypeNumber,
				LabelKey: "cronjobs.args.sync.max_depth",
				HelpKey:  "cronjobs.args.sync.max_depth_help",
				Default:  "0",
			},
			{
				Name:     "ignore_existing",
				Type:     ArgFieldTypeBool,
				LabelKey: "cronjobs.args.sync.ignore_existing",
				HelpKey:  "cronjobs.args.sync.ignore_existing_help",
				Default:  "false",
			},
			{
				Name:     "max_size",
				Type:     ArgFieldTypeNumber,
				LabelKey: "cronjobs.args.sync.max_size",
				HelpKey:  "cronjobs.args.sync.max_size_help",
				Default:  "0",
			},
			{
				Name:     "min_size",
				Type:     ArgFieldTypeNumber,
				LabelKey: "cronjobs.args.sync.min_size",
				HelpKey:  "cronjobs.args.sync.min_size_help",
				Default:  "0",
			},
			{
				Name:     "max_age",
				Type:     ArgFieldTypeString,
				LabelKey: "cronjobs.args.sync.max_age",
				HelpKey:  "cronjobs.args.sync.max_age_help",
			},
			{
				Name:     "min_age",
				Type:     ArgFieldTypeString,
				LabelKey: "cronjobs.args.sync.min_age",
				HelpKey:  "cronjobs.args.sync.min_age_help",
			},
			{
				Name:     "exclude",
				Type:     ArgFieldTypeLines,
				LabelKey: "cronjobs.args.sync.exclude",
				HelpKey:  "cronjobs.args.sync.exclude_help",
			},
			{
				Name:     "exclude_regexp",
				Type:     ArgFieldTypeLines,
				LabelKey: "cronjobs.args.sync.exclude_regexp",
				HelpKey:  "cronjobs.args.sync.exclude_regexp_help",
			},
			{
				Name:     "exclude_regexp2",
				Type:     ArgFieldTypeLines,
				LabelKey: "cronjobs.args.sync.exclude_regexp2",
				HelpKey:  "cronjobs.args.sync.exclude_regexp2_help",
			},
			{
				Name:     "size",
				Type:     ArgFieldTypeBool,
				LabelKey: "cronjobs.args.sync.size",
				HelpKey:  "cronjobs.args.sync.size_help",
				Default:  "true",
			},
			{
				Name:     "mtime",
				Type:     ArgFieldTypeBool,
				LabelKey: "cronjobs.args.sync.mtime",
				HelpKey:  "cronjobs.args.sync.mtime_help",
				Default:  "false",
			},
			{
				Name:     "checksum",
				Type:     ArgFieldTypeBool,
				LabelKey: "cronjobs.args.sync.checksum",
				HelpKey:  "cronjobs.args.sync.checksum_help",
				Default:  "false",
			},
		},
	}
}
