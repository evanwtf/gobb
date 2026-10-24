-- Gives two members of docs/example-bundle/ an argon2id hash at m=65536, t=4,
-- p=2, the most costly kind that an imported board holds. The hash is of a
-- made-up password that no test sends, so every sign-in by these names is a
-- wrong password that runs a 64 MiB check. CI runs this after the import and
-- before gobb starts, so the start-up charge and floor see the hash.
update users
   set password_scheme = 'legacy',
       password_hash = '$argon2id$v=19$m=65536,t=4,p=2$YwFmSjuMR08TCyfkxhf8bw$nTXvbEajMx0+qkd1X9IMiLm1JAE8GKIMNGBBYPaNK84'
 where username in ('bob', 'carol');
