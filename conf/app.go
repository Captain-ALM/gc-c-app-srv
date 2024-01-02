package conf

import "time"

type AppYaml struct {
	MaxQuizzes         uint32        `yaml:"maxQuizzes"`
	MaxQuestions       uint32        `yaml:"maxQuestions"`
	MaxAnswers         uint32        `yaml:"maxAnswers"`
	MaxGuests          uint32        `yaml:"maxGuests"`
	ConnectionLifetime time.Duration `yaml:"connectionLifetime"`
	GameLifetime       time.Duration `yaml:"gameLifetime"`
	Timeout            time.Duration `yaml:"timeout"`
	LifetimePolltime   time.Duration `yaml:"lifetimePolltime"`
	OAuthAudiences     []string      `yaml:"oAuthAudiences"`
}

func (ay AppYaml) GetMaxQuizzes() int {
	if ay.MaxQuizzes == 0 || ay.MaxQuizzes > 8192 {
		return 8192
	}
	return int(ay.MaxQuizzes)
}

func (ay AppYaml) GetMaxQuestions() int {
	if ay.MaxQuestions == 0 || ay.MaxQuestions > 128 {
		return 128
	}
	return int(ay.MaxQuestions)
}

func (ay AppYaml) GetMaxAnswers() int {
	if ay.MaxAnswers == 0 || ay.MaxAnswers > 8 {
		return 8
	}
	return int(ay.MaxAnswers)
}

func (ay AppYaml) GetMaxGuests() int {
	if ay.MaxGuests == 0 || ay.MaxGuests > 64 {
		return 64
	}
	return int(ay.MaxGuests)
}

func (ay AppYaml) GetConnectionLifetime() time.Duration {
	if ay.ConnectionLifetime <= 0 {
		return time.Hour * 8784
	} else if ay.ConnectionLifetime < time.Minute {
		return time.Minute
	}
	return ay.ConnectionLifetime
}

func (ay AppYaml) GetGameLifetime() time.Duration {
	if ay.GameLifetime <= 0 {
		return time.Hour * 2
	} else if ay.GameLifetime < time.Minute {
		return time.Minute
	}
	return ay.GameLifetime
}

func (ay AppYaml) GetTimeout() time.Duration {
	if ay.Timeout < 0 {
		return time.Second
	} else {
		return ay.Timeout
	}
}

func (ay AppYaml) GetLifetimePolltime() time.Duration {
	lPollt := ay.LifetimePolltime
	cLT := ay.GetConnectionLifetime()
	if lPollt > cLT {
		lPollt = cLT / 2
	}
	gLT := ay.GetGameLifetime()
	if lPollt > gLT {
		lPollt = gLT / 2
	}
	if lPollt <= 0 {
		return time.Second / 2
	}
	return lPollt
}
