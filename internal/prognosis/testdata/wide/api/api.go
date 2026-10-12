package api

func Open() int  { return helper() }
func Close() int { return 2 }
func Read() int  { return 3 }
func Write() int { return 4 }
func Seek() int  { return 5 }
func Stat() int  { return 6 }
func Sync() int  { return 7 }

func helper() int { return 1 }
