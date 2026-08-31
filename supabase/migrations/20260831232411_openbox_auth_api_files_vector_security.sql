create table if not exists public.openbox_profiles (
  id uuid primary key references auth.users(id) on delete cascade,
  display_name text,
  avatar_path text,
  role text not null default 'user' check (role in ('owner','admin','user')),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table if not exists public.openbox_integrations (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  provider text not null,
  status text not null default 'disconnected' check (status in ('connected','disconnected','error')),
  credential_ref text,
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (user_id, provider)
);

create table if not exists public.openbox_files (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  storage_provider text not null,
  bucket text,
  object_path text not null,
  checksum text,
  size_bytes bigint check (size_bytes is null or size_bytes >= 0),
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  unique (storage_provider, bucket, object_path)
);

create table if not exists public.openbox_embeddings (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references auth.users(id) on delete cascade,
  file_id uuid references public.openbox_files(id) on delete cascade,
  chunk_index integer not null default 0 check (chunk_index >= 0),
  content text not null,
  embedding vector(1536),
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  unique (file_id, chunk_index)
);

create index if not exists openbox_integrations_user_idx on public.openbox_integrations(user_id);
create index if not exists openbox_files_user_idx on public.openbox_files(user_id);
create index if not exists openbox_embeddings_user_idx on public.openbox_embeddings(user_id);
create index if not exists openbox_embeddings_file_idx on public.openbox_embeddings(file_id);
create index if not exists openbox_embeddings_hnsw_idx on public.openbox_embeddings using hnsw (embedding vector_cosine_ops);

alter table public.openbox_profiles enable row level security;
alter table public.openbox_integrations enable row level security;
alter table public.openbox_files enable row level security;
alter table public.openbox_embeddings enable row level security;

create policy "profiles_select_own" on public.openbox_profiles for select to authenticated using (id = auth.uid());
create policy "profiles_update_own" on public.openbox_profiles for update to authenticated using (id = auth.uid()) with check (id = auth.uid());
create policy "integrations_own_all" on public.openbox_integrations for all to authenticated using (user_id = auth.uid()) with check (user_id = auth.uid());
create policy "files_own_all" on public.openbox_files for all to authenticated using (user_id = auth.uid()) with check (user_id = auth.uid());
create policy "embeddings_own_all" on public.openbox_embeddings for all to authenticated using (user_id = auth.uid()) with check (user_id = auth.uid());

create or replace function public.openbox_handle_new_user()
returns trigger
language plpgsql
security definer
set search_path = public
as $$
begin
  insert into public.openbox_profiles (id, display_name, avatar_path)
  values (
    new.id,
    coalesce(new.raw_user_meta_data->>'full_name', new.raw_user_meta_data->>'user_name', split_part(coalesce(new.email,''),'@',1)),
    new.raw_user_meta_data->>'avatar_url'
  )
  on conflict (id) do nothing;
  return new;
end;
$$;

revoke all on function public.openbox_handle_new_user() from public, anon, authenticated;
grant execute on function public.openbox_handle_new_user() to postgres, supabase_auth_admin;

drop trigger if exists on_auth_user_created_openbox on auth.users;
create trigger on_auth_user_created_openbox
after insert on auth.users
for each row execute function public.openbox_handle_new_user();

revoke execute on function public.rls_auto_enable() from public, anon, authenticated;
