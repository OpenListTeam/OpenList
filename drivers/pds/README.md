# PDS Driver

Native OpenList driver for Aliyun PDS.

## Supported Operations

- List files and folders
- Resolve file metadata by path
- Generate direct download links
- Upload files with one-part upload
- Create folders
- Rename files and folders
- Move files and folders
- Copy files and folders
- Move files and folders to recycle bin
- Read drive usage details
- Refresh and persist OAuth tokens when `refresh_token` is configured

Deletion uses the verified `/v2/recyclebin/trash` endpoint, so OpenList delete operations move objects to the PDS recycle bin instead of permanently deleting them.

## Storage Fields

- `root_folder_id`: root folder id, default `root`
- `domain_id`: PDS domain id
- `drive_id`: target drive id
- `client_id`: OAuth client id, default `lMNVp25Sd1MfqZDQ`
- `access_token`: short-lived PDS access token; either `access_token` or `refresh_token` is required
- `refresh_token`: optional token used for automatic refresh; either `access_token` or `refresh_token` is required
- `token_type`: usually `Bearer`
- `expires_at`: Unix timestamp in seconds; set `0` to let the driver refresh on first request when `refresh_token` is present

## Notes

- The driver calls PDS APIs directly from Go and does not execute the Python script at runtime.
- Upload uses PDS `/v2/file/create`, presigned `PUT`, and `/v2/file/complete`.
- Download links are requested through `/v2/file/get` and cached for two hours by OpenList.
