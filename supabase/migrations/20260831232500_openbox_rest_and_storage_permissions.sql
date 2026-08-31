revoke all on table
  public.x_storages, public.x_users, public.x_meta, public.x_setting_items,
  public.x_search_nodes, public.x_task_items, public.x_ssh_public_keys,
  public.x_sharing_dbs, public.openbox_job_runs, public.openbox_backup_manifest
from anon, authenticated;

revoke all on table public.openbox_profiles, public.openbox_integrations,
  public.openbox_files, public.openbox_embeddings from anon;

grant select, update on public.openbox_profiles to authenticated;
grant select, insert, update, delete on public.openbox_integrations to authenticated;
grant select, insert, update, delete on public.openbox_files to authenticated;
grant select, insert, update, delete on public.openbox_embeddings to authenticated;

create policy "openbox_files_read_own" on storage.objects for select to authenticated
using (bucket_id = 'open-box-files' and (storage.foldername(name))[1] = auth.uid()::text);
create policy "openbox_files_insert_own" on storage.objects for insert to authenticated
with check (bucket_id = 'open-box-files' and (storage.foldername(name))[1] = auth.uid()::text);
create policy "openbox_files_update_own" on storage.objects for update to authenticated
using (bucket_id = 'open-box-files' and (storage.foldername(name))[1] = auth.uid()::text)
with check (bucket_id = 'open-box-files' and (storage.foldername(name))[1] = auth.uid()::text);
create policy "openbox_files_delete_own" on storage.objects for delete to authenticated
using (bucket_id = 'open-box-files' and (storage.foldername(name))[1] = auth.uid()::text);
