SQLite DB implementation
========================

Cloned from OpenBazaar/wallet-interface

The `Datastore` interface allows for a pluggable wallet database.
so that wallet data can be stored in the same database alongside the rest of OpenBazaar data so that users need only make one backup.

Writing your own implementation is probably the best approach, however, this package does contain a workable `Datastore` implementation
using SQLite. This is the datastore used by `NewDefaultConfig`.