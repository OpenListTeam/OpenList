package conf

import "context"

func GetApiUrl(ctx context.Context) string {
	api, _ := ctx.Value(ApiUrlKey).(string)
	return api
}
