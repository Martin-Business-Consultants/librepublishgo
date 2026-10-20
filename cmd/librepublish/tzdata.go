package main

// The zone database, built in, so TZ (TZ=America/New_York) works in a
// container that has none: Forms' emails show dates in it.
import _ "time/tzdata"
